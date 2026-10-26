import {DevLinksTab} from '../../sandbox/DevLinks';
import {PreviewsTab} from '../../sandbox/Previews';
import type {SandboxContext} from '../../sandbox/Sandbox';
import {SectionIntro} from '../bits';

// Dev links and detected servers on one page; both keep their existing cards and decisions.
export function Previews(ctx: SandboxContext) {
  return (
    <div className="stack">
      <SectionIntro title="Previews & dev links">Dev links are apps an agent published with <code>dev-link publish</code>: scanned, approved per build, private behind Authentik. Detected servers are listeners found in the sandbox; approval exposes that process and socket on the LAN and/or Tailscale.</SectionIntro>
      <h3 className="sub-head">Dev links</h3>
      <DevLinksTab {...ctx} />
      <h3 className="sub-head">Detected servers</h3>
      <PreviewsTab {...ctx} />
    </div>
  );
}
