package githubpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// GraphQL requests are all POST /graphql, so the operation decides. The
// broker allows queries (reads, bounded by the credential's own scope) and a
// short list of mutations whose target repository it can check. Everything
// else, including subscriptions, several operations in one document and
// mutations with more than one field, is refused.
//
// Only what the decision needs is parsed: operation types, the top-level
// fields of a mutation and their single "input" argument. The lexer follows
// the GraphQL lexical grammar (October 2021 edition, section 2.1); anything
// it does not recognise fails closed.

// MaxGraphQLQuery bounds the query document.
const MaxGraphQLQuery = 256 << 10

type mutationRule struct {
	action string
	// node names the input field holding the node ID to resolve.
	node   string
	fields []string
}

var mutations = map[string]mutationRule{
	"createPullRequest": {"pull.create", "repositoryId", []string{"repositoryId", "baseRefName", "headRefName", "headRepositoryId", "title", "body", "draft", "maintainerCanModify", "clientMutationId"}},
	"createIssue":       {"issue.create", "repositoryId", []string{"repositoryId", "title", "body", "assigneeIds", "labelIds", "milestoneId", "issueTypeId", "clientMutationId"}},
	"addComment":        {"comment.create", "subjectId", []string{"subjectId", "body", "clientMutationId"}},
}

var createRepositoryInput = []string{"name", "visibility", "ownerId", "description", "homepageUrl", "hasIssuesEnabled", "hasWikiEnabled", "clientMutationId"}

type graphQLBody struct {
	Query         string                     `json:"query"`
	Variables     map[string]json.RawMessage `json:"variables"`
	OperationName *string                    `json:"operationName"`
}

// ClassifyGraphQL decides a POST /graphql body. An allowed mutation carries
// Node: the proxy must resolve it and check the repository (or, for
// createRepository's ownerId, the account) before forwarding.
func ClassifyGraphQL(p *Policy, raw []byte) Decision {
	var body graphQLBody
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
		return deny("graphql", "", "GraphQL body must be one object with query, variables and operationName")
	}
	if body.Query == "" || len(body.Query) > MaxGraphQLQuery {
		return deny("graphql", "", "GraphQL query missing or too large")
	}
	op, err := parseOperation(body.Query)
	if err != nil {
		return deny("graphql", "", err.Error())
	}
	if body.OperationName != nil && *body.OperationName != "" && *body.OperationName != op.name {
		return deny("graphql", "", "operationName does not name the document's operation")
	}
	switch op.kind {
	case "query":
		return allow("graphql.query", "", false)
	case "mutation":
	default:
		return deny("graphql", "", "GraphQL subscriptions are not brokered")
	}
	raw, ok := body.Variables[op.variable]
	var input map[string]any
	if !ok || json.Unmarshal(raw, &input) != nil || input == nil {
		return deny("graphql."+op.field, "", "mutation input must be passed as a variable object")
	}
	if op.field == "createRepository" {
		return graphQLCreateRepository(input)
	}
	rule, ok := mutations[op.field]
	if !ok {
		return deny("graphql."+op.field, "", "GraphQL mutation "+op.field+" is not brokered")
	}
	for k := range input {
		if !contains(rule.fields, k) {
			return deny(rule.action, "", "field "+k+" is not brokered for "+op.field)
		}
	}
	node, _ := input[rule.node].(string)
	if node == "" || len(node) > 200 {
		return deny(rule.action, "", rule.node+" missing")
	}
	return Decision{Verdict: Inspect, Action: rule.action, Write: true, Node: node}
}

func graphQLCreateRepository(input map[string]any) Decision {
	for k := range input {
		if !contains(createRepositoryInput, k) {
			return deny("repo.create", "", "field "+k+" is not brokered for repository creation")
		}
	}
	name, _ := input["name"].(string)
	if !ValidRepoName(name) {
		return deny("repo.create", "", "repository name missing or invalid")
	}
	visibility := strings.ToLower(asString(input["visibility"]))
	if visibility != "private" && visibility != "public" {
		return deny("repo.create", "", "visibility must be PRIVATE or PUBLIC")
	}
	owner := asString(input["ownerId"])
	if _, has := input["ownerId"]; has && (owner == "" || len(owner) > 200) {
		return deny("repo.create", "", "ownerId invalid")
	}
	return Decision{Verdict: NeedsApproval, Action: "repo.create", Write: true, Node: owner, Approval: &ApprovalRequest{Action: ActionCreateRepository, Name: name, Visibility: visibility}}
}

func asString(v any) string { s, _ := v.(string); return s }

type operation struct {
	kind, name string
	// field and variable describe a mutation's single top-level field and the
	// variable passed as its input argument.
	field, variable string
}

type token struct {
	kind  byte // 'p' punctuator, 'n' name, 'v' value (number or string)
	value string
}

// parseOperation returns the document's single operation.
func parseOperation(src string) (operation, error) {
	toks, err := lex(src)
	if err != nil {
		return operation{}, err
	}
	ps := &parser{toks: toks}
	var ops []operation
	for !ps.eof() {
		t := ps.next()
		switch {
		case t.kind == 'p' && t.value == "{":
			ps.pos--
			if err := ps.skipBalanced(); err != nil {
				return operation{}, err
			}
			ops = append(ops, operation{kind: "query"})
		case t.kind == 'n' && (t.value == "query" || t.value == "mutation" || t.value == "subscription"):
			op := operation{kind: t.value}
			if ps.peek().kind == 'n' {
				op.name = ps.next().value
			}
			if ps.peekIs("(") {
				if err := ps.skipBalanced(); err != nil {
					return operation{}, err
				}
			}
			if ps.peekIs("@") {
				return operation{}, errors.New("operation directives are not brokered")
			}
			if !ps.peekIs("{") {
				return operation{}, errors.New("malformed GraphQL operation")
			}
			if op.kind == "mutation" {
				ps.next()
				if err := ps.mutationField(&op); err != nil {
					return operation{}, err
				}
			} else if err := ps.skipBalanced(); err != nil {
				return operation{}, err
			}
			ops = append(ops, op)
		case t.kind == 'n' && t.value == "fragment":
			// fragment Name on Type [Directives] SelectionSet
			if ps.next().kind != 'n' || ps.next().value != "on" || ps.next().kind != 'n' {
				return operation{}, errors.New("malformed GraphQL fragment")
			}
			for ps.peekIs("@") {
				ps.next()
				if ps.next().kind != 'n' {
					return operation{}, errors.New("malformed GraphQL fragment")
				}
				if ps.peekIs("(") {
					if err := ps.skipBalanced(); err != nil {
						return operation{}, err
					}
				}
			}
			if !ps.peekIs("{") {
				return operation{}, errors.New("malformed GraphQL fragment")
			}
			if err := ps.skipBalanced(); err != nil {
				return operation{}, err
			}
		default:
			return operation{}, errors.New("only GraphQL operations and fragments are brokered")
		}
	}
	if len(ops) != 1 {
		return operation{}, errors.New("a GraphQL document must hold exactly one operation")
	}
	return ops[0], nil
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) eof() bool { return p.pos >= len(p.toks) }
func (p *parser) next() token {
	if p.eof() {
		p.pos++
		return token{}
	}
	t := p.toks[p.pos]
	p.pos++
	return t
}
func (p *parser) peek() token {
	if p.eof() {
		return token{}
	}
	return p.toks[p.pos]
}
func (p *parser) peekIs(punct string) bool {
	t := p.peek()
	return t.kind == 'p' && t.value == punct
}

// skipBalanced consumes one bracketed group starting at the current token.
func (p *parser) skipBalanced() error {
	var stack []string
	closing := map[string]string{"{": "}", "(": ")", "[": "]"}
	for {
		if p.eof() {
			return errors.New("unbalanced GraphQL document")
		}
		t := p.next()
		if t.kind != 'p' {
			if len(stack) == 0 {
				return errors.New("malformed GraphQL document")
			}
			continue
		}
		if c, ok := closing[t.value]; ok {
			stack = append(stack, c)
			continue
		}
		if t.value == "}" || t.value == ")" || t.value == "]" {
			if len(stack) == 0 || stack[len(stack)-1] != t.value {
				return errors.New("unbalanced GraphQL document")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return nil
			}
		} else if len(stack) == 0 {
			return errors.New("malformed GraphQL document")
		}
	}
}

// mutationField parses a mutation's top-level selection set after its "{":
// exactly one field, optionally aliased, whose only argument is
// "input: $variable", with no directives.
func (p *parser) mutationField(op *operation) error {
	fields := 0
	for {
		t := p.next()
		if t.kind == 'p' && t.value == "}" {
			break
		}
		if t.kind != 'n' {
			return errors.New("mutations must select fields directly, without fragments")
		}
		name := t.value
		if p.peekIs(":") {
			p.next()
			if t = p.next(); t.kind != 'n' {
				return errors.New("malformed GraphQL alias")
			}
			name = t.value
		}
		if !p.peekIs("(") {
			return errors.New("mutation " + name + " needs an input argument")
		}
		p.next()
		if a := p.next(); a.kind != 'n' || a.value != "input" {
			return errors.New("mutation " + name + " must take only input: $variable")
		}
		if c := p.next(); c.kind != 'p' || c.value != ":" {
			return errors.New("malformed GraphQL argument")
		}
		if d := p.next(); d.kind != 'p' || d.value != "$" {
			return errors.New("mutation input must be passed as a variable")
		}
		v := p.next()
		if v.kind != 'n' {
			return errors.New("malformed GraphQL variable")
		}
		if c := p.next(); c.kind != 'p' || c.value != ")" {
			return errors.New("mutation " + name + " must take only input: $variable")
		}
		if p.peekIs("@") {
			return errors.New("mutation directives are not brokered")
		}
		if p.peekIs("{") {
			if err := p.skipBalanced(); err != nil {
				return err
			}
		}
		op.field, op.variable = name, v.value
		fields++
	}
	if fields != 1 {
		return errors.New("a mutation must hold exactly one field")
	}
	return nil
}

// lex splits src into tokens, dropping ignored tokens (whitespace, line
// terminators, commas, comments and a byte order mark).
func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case strings.HasPrefix(src[i:], "\uFEFF"):
			i += len("\uFEFF")
		case c == '#':
			for i < len(src) && src[i] != '\n' && src[i] != '\r' {
				i++
			}
		case strings.HasPrefix(src[i:], "..."):
			out = append(out, token{'p', "..."})
			i += 3
		case strings.ContainsRune("!$&()[]{}:=@|", rune(c)):
			out = append(out, token{'p', string(c)})
			i++
		case c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
			j := i + 1
			for j < len(src) && (src[j] == '_' || src[j] >= '0' && src[j] <= '9' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= 'a' && src[j] <= 'z') {
				j++
			}
			out = append(out, token{'n', src[i:j]})
			i = j
		case c == '-' || c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || strings.ContainsRune(".eE+-", rune(src[j]))) {
				j++
			}
			out = append(out, token{'v', src[i:j]})
			i = j
		case strings.HasPrefix(src[i:], `"""`):
			end := blockStringEnd(src, i+3)
			if end < 0 {
				return nil, errors.New("unterminated GraphQL block string")
			}
			out = append(out, token{'v', "block"})
			i = end + 3
		case c == '"':
			j := i + 1
			for ; j < len(src) && src[j] != '"'; j++ {
				if src[j] == '\n' || src[j] == '\r' {
					return nil, errors.New("unterminated GraphQL string")
				}
				if src[j] == '\\' {
					j++
				}
			}
			if j >= len(src) {
				return nil, errors.New("unterminated GraphQL string")
			}
			out = append(out, token{'v', "string"})
			i = j + 1
		default:
			return nil, errors.New("unexpected character in GraphQL document")
		}
	}
	return out, nil
}

// blockStringEnd finds the closing """ of a block string, honouring \""".
func blockStringEnd(src string, i int) int {
	for i < len(src) {
		if strings.HasPrefix(src[i:], `\"""`) {
			i += 4
			continue
		}
		if strings.HasPrefix(src[i:], `"""`) {
			return i
		}
		i++
	}
	return -1
}
