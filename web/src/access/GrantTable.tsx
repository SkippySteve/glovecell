// One table pattern for every fine-grained access editor (Internet requests
// and web rules, LAN hosts, packages, credentials, project HTTPS rules):
// search, filter chips, bulk select with bulk actions, an expiry column and an
// empty state that says what to do. On phones each row becomes a card.
import {useEffect, useId, useMemo, useState, type ReactNode} from 'react';
import {Button, Input} from '../ui';
import {clockAt, left, stamp} from '../workspace/time';
import './access.css';

export interface GrantColumn<T> { key: string; label: string; cell: (row: T) => ReactNode; className?: string }
export interface GrantFilter<T> { key: string; label: string; test: (row: T) => boolean }
export interface BulkAction<T> {
  key: string; label: string; danger?: boolean;
  // Rows the action applies to; others are skipped (and the count says so).
  applies?: (row: T) => boolean;
  run: (rows: T[]) => Promise<unknown> | unknown;
}
// When a grant ends: an absolute time, never (until someone removes it), or a note.
export type Expiry = {at: number} | 'never' | {note: string};
type Search<T> = {placeholder: string; label?: string; text: (row: T) => string} | {placeholder: string; label?: string; value: string; onChange: (value: string) => void};

export interface GrantTableProps<T> {
  title: ReactNode;
  label: string;
  rows: T[];
  rowKey: (row: T) => string;
  // Names a row for screen readers (the selection checkbox); defaults to its key.
  rowLabel?: (row: T) => string;
  columns: GrantColumn<T>[];
  now: number;
  expiry?: (row: T) => Expiry;
  search?: Search<T>;
  filters?: GrantFilter<T>[];
  bulk?: BulkAction<T>[];
  actions?: (row: T, row_: {expanded: boolean; toggle: () => void}) => ReactNode;
  detail?: (row: T) => ReactNode | null;
  empty: {title: string; hint: ReactNode; action?: ReactNode};
  intro?: ReactNode;
  toolbar?: ReactNode;
  footer?: ReactNode;
  loading?: boolean;
  testId?: string;
  // Initially open row (for example a host preselected by a blocked report).
  initiallyOpen?: string;
}

const SOON_MS = 15 * 60 * 1000;

export function ExpiryCell({expiry, now}: {expiry: Expiry; now: number}) {
  if (expiry === 'never') return <span className="muted">Until removed</span>;
  if ('note' in expiry) return <span className="muted">{expiry.note}</span>;
  if (!expiry.at) return <span className="muted">Until removed</span>;
  if (expiry.at <= now) return <span className="expiry is-over">Expired</span>;
  const soon = expiry.at - now < SOON_MS;
  const sameDay = new Date(expiry.at).toDateString() === new Date(now).toDateString();
  return (
    <span className={'expiry' + (soon ? ' is-soon' : '')}>
      <span className="num">{left(expiry.at, now)}</span> <span className="subtle">left, until {sameDay ? clockAt(expiry.at) : stamp(expiry.at)}</span>
    </span>
  );
}

export function GrantTable<T>(props: GrantTableProps<T>) {
  const {title, label, rows, rowKey, rowLabel, columns, now, expiry, search, filters, bulk, actions, detail, empty, intro, toolbar, footer, loading, testId, initiallyOpen} = props;
  const id = useId();
  const [local, setLocal] = useState('');
  const [filter, setFilter] = useState('all');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState(initiallyOpen || '');
  const [busy, setBusy] = useState('');
  const remote = search && 'onChange' in search;
  const query = remote ? search.value : local;
  const shown = useMemo(() => {
    const f = filters?.find(x => x.key === filter);
    const q = query.trim().toLowerCase();
    return rows.filter(r => (!f || f.test(r)) && (remote || !q || !search || !('text' in search) || search.text(r).toLowerCase().includes(q)));
  }, [rows, filters, filter, query, remote, search]);
  const keys = shown.map(rowKey);
  // Selection follows what is shown: rows that disappear are deselected.
  useEffect(() => { setSelected(old => { const next = new Set([...old].filter(k => keys.includes(k))); return next.size === old.size ? old : next; }); }, [keys.join('\n')]); // keys is derived from shown
  const selectable = Boolean(bulk?.length);
  const picked = shown.filter(r => selected.has(rowKey(r)));
  const all = shown.length > 0 && picked.length === shown.length;
  const toggleAll = () => setSelected(all ? new Set() : new Set(keys));
  const toggle = (k: string) => setSelected(old => { const next = new Set(old); if (next.has(k)) next.delete(k); else next.add(k); return next; });
  const runBulk = async (a: BulkAction<T>) => {
    const target = picked.filter(r => !a.applies || a.applies(r));
    if (!target.length) return;
    setBusy(a.key);
    try { await a.run(target); setSelected(new Set()); } finally { setBusy(''); }
  };
  const cols = columns.length + (selectable ? 1 : 0) + (expiry ? 1 : 0) + (actions ? 1 : 0);
  const filtered = query.trim() || filter !== 'all';
  return (
    <section className="card grant-table" aria-labelledby={id + 'title'} data-testid={testId}>
      <div className="card-hd">
        <h3 id={id + 'title'}>{title}</h3>
        {toolbar ? <span className="end">{toolbar}</span> : null}
      </div>
      {intro ? <div className="grant-intro">{intro}</div> : null}
      {search || filters?.length ? (
        <div className="grant-tools">
          {search ? <div className="field grow">
            <label className="field-label" htmlFor={id + 'q'}>{search.label || 'Search'}</label>
            <Input id={id + 'q'} type="search" value={query} placeholder={search.placeholder} autoCapitalize="off" spellCheck={false}
              onChange={e => remote ? search.onChange(e.target.value) : setLocal(e.target.value)} />
          </div> : null}
          {filters?.length ? <div className="grant-filters" role="group" aria-label={'Show ' + label}>
            {[{key: 'all', label: 'All', test: () => true} as GrantFilter<T>, ...filters].map(f => {
              const n = f.key === 'all' ? rows.length : rows.filter(f.test).length;
              return <button key={f.key} type="button" className="filter-chip" aria-pressed={filter === f.key} onClick={() => setFilter(f.key)}>{f.label} <span className="num">{n}</span></button>;
            })}
          </div> : null}
        </div>
      ) : null}
      {selectable && picked.length ? (
        <div className="bulk-bar" role="region" aria-label="Bulk actions">
          <strong className="num">{picked.length} selected</strong>
          {bulk!.map(a => {
            const n = picked.filter(r => !a.applies || a.applies(r)).length;
            return <Button key={a.key} size="small" variant={a.danger ? 'secondary' : 'primary'} buttonType={a.danger ? 'destructive' : undefined} disabled={!n || Boolean(busy)} onClick={() => runBulk(a)}>
              {a.label}{n !== picked.length ? ` (${n})` : ''}
            </Button>;
          })}
          <Button size="small" variant="tertiary" onClick={() => setSelected(new Set())}>Clear selection</Button>
        </div>
      ) : null}
      <div className="grant-scroll">
        <table className="grant-grid" aria-label={label}>
          <thead>
            <tr>
              {selectable ? <th scope="col" className="sel"><input type="checkbox" checked={all} disabled={!shown.length} aria-label={all ? 'Clear selection' : 'Select all shown'} onChange={toggleAll} /></th> : null}
              {columns.map(c => <th key={c.key} scope="col" className={c.className}>{c.label}</th>)}
              {expiry ? <th scope="col" className="exp">Expires</th> : null}
              {actions ? <th scope="col" className="act"><span className="visually-hidden">Actions</span></th> : null}
            </tr>
          </thead>
          <tbody>
            {shown.map(r => {
              const k = rowKey(r), expanded = open === k, extra = expanded && detail ? detail(r) : null;
              return [
                <tr key={k} className={(selected.has(k) ? 'is-selected' : '') + (expanded ? ' is-open' : '')}>
                  {selectable ? <td className="sel"><input type="checkbox" checked={selected.has(k)} aria-label={'Select ' + (rowLabel ? rowLabel(r) : k)} onChange={() => toggle(k)} /></td> : null}
                  {columns.map(c => <td key={c.key} className={c.className} data-label={c.label}>{c.cell(r)}</td>)}
                  {expiry ? <td className="exp" data-label="Expires"><ExpiryCell expiry={expiry(r)} now={now} /></td> : null}
                  {actions ? <td className="act">{actions(r, {expanded, toggle: () => setOpen(expanded ? '' : k)})}</td> : null}
                </tr>,
                extra ? <tr key={k + ' detail'} className="grant-detail"><td colSpan={cols}>{extra}</td></tr> : null,
              ];
            })}
          </tbody>
        </table>
        {!shown.length ? (
          <div className="grant-empty" role="status">
            {loading ? <p>Loading…</p> : filtered ? <>
              <p><strong>Nothing matches.</strong></p>
              <p className="muted">Clear the search or choose All.</p>
            </> : <>
              <p><strong>{empty.title}</strong></p>
              <p className="muted">{empty.hint}</p>
              {empty.action ? <div>{empty.action}</div> : null}
            </>}
          </div>
        ) : null}
      </div>
      {footer ? <div className="grant-foot">{footer}</div> : null}
    </section>
  );
}
