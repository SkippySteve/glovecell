package cellrelay

import (
	"errors"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// The relay never unmarshals a whole OTLP request into generated structs: a
// 1 MiB body of empty records would allocate hundreds of megabytes. It walks
// the protobuf wire format instead, reading only the few fields it keeps and
// skipping the rest in place, so memory stays O(1) per request and CPU stays
// linear in the (bounded) body.

var errWire = errors.New("invalid OTLP protobuf")

// MaxRecords bounds the records (metrics' data points and log records)
// processed from one request; the rest of the request is refused.
const MaxRecords = 4096

type field struct {
	num   protowire.Number
	typ   protowire.Type
	bytes []byte
	u     uint64
}

// each calls fn for every top-level field of message b.
func each(b []byte, fn func(field) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return errWire
		}
		b = b[n:]
		f := field{num: num, typ: typ}
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return errWire
			}
			f.u, n = v, m
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				return errWire
			}
			f.u, n = v, m
		case protowire.Fixed32Type:
			v, m := protowire.ConsumeFixed32(b)
			if m < 0 {
				return errWire
			}
			f.u, n = uint64(v), m
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return errWire
			}
			f.bytes, n = v, m
		default: // groups are not used by OTLP
			return errWire
		}
		b = b[n:]
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

// within calls fn for each occurrence of the nested message path in b,
// e.g. within(req, fn, 1, 2, 2) visits resource_logs.scope_logs.log_records.
func within(b []byte, fn func([]byte) error, path ...protowire.Number) error {
	if len(path) == 0 {
		return fn(b)
	}
	return each(b, func(f field) error {
		if f.num == path[0] && f.typ == protowire.BytesType {
			return within(f.bytes, fn, path[1:]...)
		}
		return nil
	})
}

// attrs reads the string or bool values of the wanted keys from repeated
// KeyValue field num of message b. Other keys and value kinds are skipped.
func attrs(b []byte, num protowire.Number, wanted map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	err := each(b, func(f field) error {
		if f.num != num || f.typ != protowire.BytesType {
			return nil
		}
		var key string
		var value []byte
		if err := each(f.bytes, func(kv field) error {
			switch {
			case kv.num == 1 && kv.typ == protowire.BytesType:
				key = string(kv.bytes)
			case kv.num == 2 && kv.typ == protowire.BytesType:
				value = kv.bytes
			}
			return nil
		}); err != nil {
			return err
		}
		if !wanted[key] || value == nil {
			return nil
		}
		return each(value, func(av field) error {
			switch {
			case av.num == 1 && av.typ == protowire.BytesType && len(av.bytes) <= 128: // string_value
				out[key] = string(av.bytes)
			case av.num == 2 && av.typ == protowire.VarintType: // bool_value
				out[key] = map[bool]string{true: "true", false: "false"}[av.u != 0]
			}
			return nil
		})
	})
	return out, err
}

type dataPoint struct {
	value float64
	attrs map[string]string
}

// reduceMetrics walks an ExportMetricsServiceRequest and hands each known
// delta Sum's data points to keep.
func (r *Relay) reduceMetrics(b []byte) error {
	records := 0
	return within(b, func(metric []byte) error {
		var name string
		var sum []byte
		if err := each(metric, func(f field) error {
			switch {
			case f.num == 1 && f.typ == protowire.BytesType && len(f.bytes) <= 128:
				name = string(f.bytes)
			case f.num == 7 && f.typ == protowire.BytesType:
				sum = f.bytes
			}
			return nil
		}); err != nil {
			return err
		}
		rule, known := metricRules[name]
		comp, _ := component(name)
		if !known || sum == nil || comp == "" {
			r.dropped.WithLabelValues("unknown_metric").Inc()
			return nil
		}
		wanted := map[string]bool{}
		for in := range rule.attrs {
			wanted[in] = true
		}
		temporality := uint64(0)
		points := []dataPoint{}
		if err := each(sum, func(f field) error {
			switch {
			case f.num == 2 && f.typ == protowire.VarintType:
				temporality = f.u
			case f.num == 1 && f.typ == protowire.BytesType:
				records++
				if records > MaxRecords {
					return errTooMany
				}
				p := dataPoint{}
				if err := each(f.bytes, func(d field) error {
					switch {
					case d.num == 4 && d.typ == protowire.Fixed64Type: // as_double
						p.value = math.Float64frombits(d.u)
					case d.num == 6 && d.typ == protowire.Fixed64Type: // as_int (sfixed64)
						p.value = float64(int64(d.u))
					}
					return nil
				}); err != nil {
					return err
				}
				a, err := attrs(f.bytes, 7, wanted)
				if err != nil {
					return err
				}
				p.attrs = a
				points = append(points, p)
			}
			return nil
		}); err != nil {
			return err
		}
		if temporality != 1 { // AGGREGATION_TEMPORALITY_DELTA
			r.dropped.WithLabelValues("cumulative").Inc()
			return nil
		}
		for _, p := range points {
			r.keepMetric(rule, comp, p)
		}
		return nil
	}, 1, 2, 2)
}

// reduceLogs walks an ExportLogsServiceRequest and counts known agent events.
func (r *Relay) reduceLogs(b []byte) error {
	records := 0
	return within(b, func(scopeLogs []byte) error {
		scopeName := ""
		if err := each(scopeLogs, func(f field) error {
			if f.num == 1 && f.typ == protowire.BytesType {
				return each(f.bytes, func(s field) error {
					if s.num == 1 && s.typ == protowire.BytesType && len(s.bytes) <= 128 {
						scopeName = string(s.bytes)
					}
					return nil
				})
			}
			return nil
		}); err != nil {
			return err
		}
		return each(scopeLogs, func(f field) error {
			if f.num != 2 || f.typ != protowire.BytesType {
				return nil
			}
			records++
			if records > MaxRecords {
				return errTooMany
			}
			eventName := ""
			if err := each(f.bytes, func(l field) error {
				if l.num == 12 && l.typ == protowire.BytesType && len(l.bytes) <= 128 {
					eventName = string(l.bytes)
				}
				return nil
			}); err != nil {
				return err
			}
			a, err := attrs(f.bytes, 6, map[string]bool{"event.name": true, "success": true})
			if err != nil {
				return err
			}
			if eventName == "" {
				eventName = a["event.name"]
			}
			r.keepEvent(scopeName, eventName, a["success"])
			return nil
		})
	}, 1, 2)
}

var errTooMany = errors.New("too many records")
