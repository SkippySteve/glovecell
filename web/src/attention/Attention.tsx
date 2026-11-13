// The "Needs attention" inbox (#/attention): everything that waits on the
// operator across sandboxes, projects and runs, grouped by urgency, with the
// one-click actions the per-sandbox pages already offer and their confirmations.
import {useEffect, useState, type ReactNode} from 'react';
import {sandboxAction} from '../api';
import {useData} from '../data';
import {runHref} from '../factory/href';
import {plainText, REASON_LIMIT} from '../orca';
import {hashQuery, projectHref, replaceHashQuery, sandboxHref, withQuery} from '../router';
import {Button, Callout, Tag, type TagVariant} from '../ui';
import {useConfirm} from '../workspace/Confirm';
import {setLanPrefill} from '../workspace/prefill';
import {decideRequest, webLearn} from '../workspace/sources/internet';
import {lanExtend, lanRestrict, windowLabel} from '../workspace/sources/lan';
import {dismissReport} from '../workspace/sources/reports';
import {ago, clockAt, left, useNow} from '../workspace/time';
import {ATTENTION_FILTERS, attentionCount, attentionFilter, filterAttention, groupAttention, type AttentionFilter, type AttentionItem, type AttentionKind} from './collect';
import {refreshAttention, useAttention} from './store';
import './attention.css';

const KIND: Record<AttentionKind, [string, TagVariant]> = {
  'sandbox-error': ['Sandbox error', 'danger'], 'project-error': ['Project error', 'danger'], 'revision-error': ['Policy error', 'danger'],
  report: ['Agent blocked', 'warning'], request: ['Web request', 'info'], package: ['Package', 'info'], 'more-requests': ['Web requests', 'info'],
  devlink: ['Dev link', 'info'], preview: ['Preview', 'info'], gate: ['Run decision', 'warning'],
  'run-failed': ['Run failed', 'danger'], 'run-stuck': ['Run stuck', 'warning'], 'web-window': ['Web access', 'danger'], 'lan-grant': ['LAN grant', 'danger'],
};

// Runs one sandbox-control action, says what happened, then re-reads the list
// and the inbox, like useSandbox's act.
function useAct() {
  const {toast, reload} = useData();
  return async (id: string, path: string, fields: Record<string, string | string[]>, done: string) => {
    try {
      await sandboxAction(path, id, fields);
      toast(id + ': ' + done);
      await Promise.all([reload(), refreshAttention()]);
      return true;
    } catch (e) { toast((e as Error).message, true); return false; }
  };
}

function ownerLink(item: AttentionItem): {href: string; label: string} {
  const o = item.owner;
  if (o.kind === 'run') return {href: runHref(o.id), label: o.host || o.id};
  if (o.kind === 'project') return {href: projectHref(o.id), label: o.id};
  const section = item.kind === 'request' || item.kind === 'package' || item.kind === 'more-requests' || item.kind === 'web-window' ? 'internet'
    : item.kind === 'lan-grant' ? 'lan' : item.kind === 'devlink' || item.kind === 'preview' ? 'previews' : item.kind === 'revision-error' ? 'settings' : 'overview';
  return {href: sandboxHref(o.id, section), label: o.id};
}

function Actions({item}: {item: AttentionItem}) {
  const act = useAct();
  const ask = useConfirm();
  const {egress} = useData();
  const [busy, setBusy] = useState(false);
  const id = item.owner.id;
  const run = (fn: () => Promise<unknown>) => async () => { setBusy(true); try { await fn(); } finally { setBusy(false); } };
  const out: ReactNode[] = [];
  const link = (key: string, href: string, text: string, onClick?: () => void) => out.push(<a key={key} className="link-button" href={href} onClick={onClick}>{text}</a>);

  switch (item.kind) {
    case 'request': case 'package': {
      const o = item.observation!;
      out.push(<Button key="approve" size="small" variant="primary" disabled={busy} onClick={run(async () => { const f = decideRequest(o, true); await act(id, f.path, f.fields, item.kind === 'package' ? 'package download approved' : 'request approved'); })}>{(o.fingerprints ?? 1) > 1 ? `Approve all ${o.fingerprints}` : 'Approve'}</Button>);
      link('find', withQuery(sandboxHref(id, item.kind === 'package' ? 'packages' : 'internet'), item.kind === 'package' ? {} : {q: o.host}), 'More ways to allow');
      break;
    }
    case 'more-requests': link('review', sandboxHref(id, 'internet'), 'Review on Internet'); break;
    case 'report': {
      const r = item.report!;
      if (item.reportLan && !item.reportLan.open) link('lan', sandboxHref(id, 'lan'), 'Open LAN access', () => setLanPrefill(id, item.reportLan!.address, r));
      if (item.reportWeb) link('web', withQuery(sandboxHref(id, 'internet'), {q: item.reportWeb.filter}), 'Find it in Internet');
      out.push(<Button key="dismiss" size="small" variant="secondary" disabled={busy} onClick={run(async () => { const f = dismissReport(r); await act(id, f.path, f.fields, 'agent report dismissed'); })}>Dismiss</Button>);
      break;
    }
    case 'devlink': {
      const l = item.devlink!;
      link('review', sandboxHref(id, 'previews'), 'Review scan');
      out.push(<Button key="deny" size="small" variant="tertiary" disabled={busy} onClick={run(() => act(id, '/devlink-decision', {name: l.Name, state: 'Denied', duration: '7d', paths_text: l.RulesText, tab: 'previews'}, `${l.Name} denied`))}>Deny</Button>);
      break;
    }
    case 'preview': {
      const p = item.preview;
      link('review', sandboxHref(id, 'previews'), 'Review');
      if (p) out.push(<Button key="deny" size="small" variant="tertiary" disabled={busy} onClick={run(() => act(id, '/decision', {id: p.id, state: 'Denied', paths_text: p.rulesText || p.proposedRulesText, tab: 'previews'}, `${p.proposedName || 'preview'} denied`))}>Deny</Button>);
      break;
    }
    case 'gate': link('decide', runHref(id), 'Decide in the run report'); break;
    case 'run-failed': case 'run-stuck': link('open', runHref(id), 'Open run'); break;
    case 'web-window': {
      const w = item.web!;
      // Extend restarts the same window from now, as "Restart open window" and
      // "Restart learning" do on the Internet page.
      out.push(<Button key="extend" size="small" variant="secondary" disabled={busy} onClick={run(async () => {
        if (w.phase === 'Open') await egress([id], 'open', w.minutes);
        else { const f = webLearn(w.minutes); await act(id, f.path, f.fields, 'learning window restarted'); }
        await refreshAttention();
      })}>Extend {windowLabel(w.minutes * 60)}</Button>);
      out.push(<Button key="lock" size="small" variant="primary" buttonType="destructive" disabled={busy} onClick={run(async () => { await egress([id], 'lock', w.minutes); await refreshAttention(); })}>Lock</Button>);
      break;
    }
    case 'lan-grant': {
      const g = item.lan!;
      if (g.state === 'Open' && g.listed) {
        const s = g.windows.includes(3600) ? 3600 : g.windows[0];
        out.push(<Button key="extend" size="small" variant="secondary" disabled={busy} onClick={run(async () => { const f = lanExtend(g.address, s, g.podUid); await act(id, f.path, f.fields, `${g.address} open for ${windowLabel(s)} from now`); })}>Extend {windowLabel(s)}</Button>);
      }
      if (g.state !== 'Restricting') out.push(<Button key="restrict" size="small" variant="primary" buttonType="destructive" disabled={busy} onClick={run(async () => {
        // The same confirmation as the LAN devices page: restricting restarts the pod.
        const ok = await ask(`Restrict ${g.address} now?`, <>
          <p>This removes the LAN rule and <strong>restarts the {id} workspace pod</strong> to close open connections.</p>
          <p className="danger-box">It ends <strong>every LAN grant of {id}</strong> ({g.open} open) and <strong>its agent sessions</strong>: running commands stop and terminals disconnect. Workspace files are preserved.</p>
          <p className="muted">Web access and dev links are not affected.</p>
        </>, 'Restrict and restart pod');
        if (!ok) return;
        const f = lanRestrict(g.address, g.podUid);
        await act(id, f.path, f.fields, `${g.address}: LAN access restricting; the workspace pod restarts`);
      })}>Restrict</Button>);
      break;
    }
    case 'sandbox-error': link('open', sandboxHref(id), 'Open sandbox'); break;
    case 'revision-error': link('open', sandboxHref(id, 'settings'), 'Open revisions'); break;
    case 'project-error': link('open', projectHref(id), 'Open project'); break;
  }
  return out.length ? <div className="attn-actions">{out}</div> : null;
}

const SINCE: Partial<Record<AttentionKind, string>> = {request: 'First seen', package: 'First seen', report: 'First reported', gate: 'Last activity', preview: 'First seen', devlink: 'Requested', 'revision-error': 'Applied', 'run-failed': 'Ended', 'run-stuck': 'Last progress', 'lan-grant': 'Opened', 'web-window': 'Opened'};

function When({item, now}: {item: AttentionItem; now: number}) {
  if (item.deadline) return <span className={'attn-when' + (item.deadline - now <= 15 * 60_000 ? ' soon' : '')}>Ends in <span className="countdown num">{left(item.deadline, now)}</span>, at {clockAt(item.deadline)}</span>;
  if (item.since) return <span className="attn-when">{SINCE[item.kind] || 'Started'} {ago(item.since, now)}</span>;
  return null;
}

function Row({item, now}: {item: AttentionItem; now: number}) {
  const [label, variant] = KIND[item.kind];
  const owner = ownerLink(item);
  const reason = item.report ? plainText(item.report.reason, REASON_LIMIT) : '';
  return (
    <li className="attn-item" data-kind={item.kind} data-key={item.key}>
      <div className="attn-tag"><Tag size="sm" variant={variant} text={label} /></div>
      <div className="attn-main">
        <strong className="attn-title">{item.title}</strong>
        {item.detail ? (item.kind === 'request' || item.kind === 'package' || item.kind === 'report' ? <code className="attn-detail">{item.detail}</code> : <span className="attn-detail">{item.detail}</span>) : null}
        {reason ? <span className="attn-quote">Agent's reason (unverified): {reason}</span> : null}
        {item.reportWeb?.note ? <span className="attn-detail warn">{item.reportWeb.note}</span> : null}
        <span className="attn-meta"><a href={owner.href}>{owner.label}</a><When item={item} now={now} /></span>
      </div>
      <Actions item={item} />
    </li>
  );
}

export function Attention() {
  const {loaded: listLoaded, session} = useData();
  const a = useAttention();
  const now = useNow();
  const [show, setShow] = useState<AttentionFilter>(() => attentionFilter(hashQuery().get('show')));
  const {count, urgent} = attentionCount(a.items);
  useEffect(() => { document.title = (count ? `(${count}) ` : '') + 'Needs attention · Sandboxes'; }, [count]);
  useEffect(() => { refreshAttention(); }, []);
  const groups = groupAttention(filterAttention(a.items, show));
  const ready = a.loaded && listLoaded;
  const errors = Object.entries(a.errors);
  const checked = [`${a.checked.sandboxes} sandbox${a.checked.sandboxes === 1 ? '' : 'es'}`, `${a.checked.projects} project${a.checked.projects === 1 ? '' : 's'}`, ...(session.runRecords ? [`${a.checked.runs} run${a.checked.runs === 1 ? '' : 's'}`] : [])];
  return (
    <div className="stack attention-page">
      <div className="page-head attn-head">
        <div>
          <h1>Needs attention</h1>
          <p className="muted" aria-live="polite">{!ready ? 'Checking every sandbox, project and run…'
            : count ? `${count} thing${count === 1 ? '' : 's'} wait${count === 1 ? 's' : ''} on you${urgent ? `, ${urgent} of them now` : ''}.`
            : 'Nothing waits on you.'}</p>
        </div>
        <div className="attn-refresh">
          <Button size="small" variant="secondary" onClick={() => refreshAttention()}>Refresh</Button>
          {a.updated ? <span className="subtle-text">Checked {checked.join(', ')} at {clockAt(a.updated)}</span> : null}
        </div>
      </div>
      <div className="seg attn-filter" role="group" aria-label="Show">
        {ATTENTION_FILTERS.map(([k, l]) => {
          const n = filterAttention(a.items, k).reduce((sum, i) => sum + i.count, 0);
          return <button key={k} type="button" className="seg-button" aria-pressed={show === k} onClick={() => { setShow(k); replaceHashQuery({show: k === 'all' ? undefined : k}); }}>{l} <span className="num">{n}</span></button>;
        })}
      </div>
      {errors.length ? <Callout variant="warning" title="Some sources could not be read" description={<ul className="attn-errors">{errors.map(([k, e]) => <li key={k}><strong>{k}</strong>: {e}</li>)}</ul>} /> : null}
      {ready && !groups.length ? (
        <div className="attn-empty">
          <h2>{show === 'all' ? 'Nothing needs you right now' : 'Nothing of this kind needs you'}</h2>
          <p className="muted">New requests, blocked agents, failed runs and grants close to ending appear here as they happen.</p>
        </div>
      ) : null}
      {groups.map(g => (
        <section key={g.tier} className={'attn-group tier-' + g.tier} aria-labelledby={'attn-' + g.tier}>
          <div className="attn-group-head">
            <h2 id={'attn-' + g.tier}>{g.title} <span className="count-badge num">{g.count}</span></h2>
            <p className="subtle-text">{g.hint}</p>
          </div>
          <ul className="attn-list">{g.items.map(item => <Row key={item.key} item={item} now={now} />)}</ul>
        </section>
      ))}
    </div>
  );
}
