import {sandboxHref, type Section} from '../../router';
import {Services, timeOf, type SandboxContext} from '../../sandbox/Sandbox';
import {Profiles, Revisions} from '../../sandbox/Settings';
import {Tag} from '../../ui';
import {SectionIntro} from '../bits';
import '../previews.css';

// Where each kind of access is changed, so Settings is also the map of every setting.
export const ELSEWHERE: [Section, string, string][] = [
  ['internet', 'Internet', 'Web access windows and approved requests'],
  ['lan', 'LAN devices', 'Time-limited access to home-network hosts'],
  ['packages', 'Packages', 'Which registry packages may be downloaded'],
  ['previews', 'Previews & dev links', 'Approve, revoke and open the apps it serves'],
  ['credentials', 'Credentials', 'Model providers, repositories and cluster grants'],
  ['downloads', 'Files', 'Download or upload files into the sandbox'],
];

// Sandbox Settings, grouped: this sandbox's saved policy first, settings that
// affect every sandbox next, and a map of the settings kept in other sections.
// Each card keeps its own behaviour.
export function Settings(ctx: SandboxContext) {
  const {detail} = ctx;
  const current = detail.revision;
  const shared = detail.services.length > 0;
  return (
    <div className="stack">
      <SectionIntro title="Settings">This sandbox's saved policy and the rules it shares with others. Access itself is changed in the sections listed at the end.</SectionIntro>

      <section className="set-group" aria-labelledby="set-policy">
        <div className="set-group-head">
          <h3 id="set-policy">Policy</h3>
          <p className="muted">Everything this sandbox may reach is one policy. Each change makes a revision you can restore; a profile copies a policy between sandboxes.</p>
        </div>
        <p className="set-current">
          <strong>Revision {current.id}</strong>
          <Tag size="sm" variant={current.error ? 'danger' : current.status === 'Active' ? 'success' : 'info'} text={current.status} />
          <span>{[current.actor, current.at ? timeOf(current.at) : ''].filter(Boolean).join(' · ')}</span>
        </p>
        <Revisions {...ctx} />
        <Profiles act={ctx.act} />
      </section>

      {shared ? (
        <section className="set-group" aria-labelledby="set-shared">
          <div className="set-group-head">
            <h3 id="set-shared">Shared services</h3>
            <p className="muted">Cluster services exposed to sandboxes. A change here applies to every sandbox's route to that service, not only this one.</p>
          </div>
          <Services key={detail.revision.id} {...ctx} />
        </section>
      ) : null}

      <section className="set-group" aria-labelledby="set-elsewhere">
        <div className="set-group-head">
          <h3 id="set-elsewhere">Other settings for {detail.id}</h3>
          <p className="muted">Access is granted where you see it in use.</p>
        </div>
        <ul className="set-elsewhere">
          {ELSEWHERE.map(([key, label, help]) => <li key={key}><a href={sandboxHref(detail.id, key)}><strong>{label}</strong><span>{help}</span></a></li>)}
        </ul>
      </section>
    </div>
  );
}
