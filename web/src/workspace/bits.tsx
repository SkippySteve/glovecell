// Small shared pieces of the workspace view.
import type {ReactNode} from 'react';
import {Callout} from '../ui';
import {left, remaining} from './time';

export const Countdown = ({until, now}: {until: number; now: number}) => <span className="countdown">{left(until, now)}</span>;

// A window's remaining share as a native progress bar (no inline styles under the CSP).
export function WindowBar({until, total, now, tone, label}: {until: number; total: number; now: number; tone: 'warn' | 'bad'; label: string}) {
  return <progress className={'window-bar ' + tone} max={100} value={remaining(until, total, now)} aria-label={label} />;
}

export function SectionIntro({title, aside, children}: {title: string; aside?: ReactNode; children?: ReactNode}) {
  return (
    <div className="section-head">
      <div className="section-title"><h2>{title}</h2>{aside ? <span className="subtle-text">{aside}</span> : null}</div>
      {children ? <p className="muted">{children}</p> : null}
    </div>
  );
}

// A part of the mockup with no console API behind it yet.
export function NotAvailable({what, needs, children}: {what: string; needs: string; children?: ReactNode}) {
  return <Callout variant="neutral" className="not-available" title={what + ': not available yet'} description={<><span className="block">{needs}</span>{children ? <span className="block">{children}</span> : null}</>} />;
}

export const plural = (n: number, word: string) => `${n.toLocaleString()} ${word}${n === 1 ? '' : 's'}`;
