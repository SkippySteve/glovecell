// The Runs tab on a sandbox page: orchestration runs collected from that sandbox.
import {useEffect, useState} from 'react';
import {useData} from '../data';
import {listRuns, type RunSummary} from './api';
import {RunTable} from './RunTable';
import {runsFor} from './runs';
import {Card, CardHeader} from './ui';

export {runsFor};

export function SandboxRuns({namespace}: {namespace: string}) {
  const {toast} = useData();
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  useEffect(() => { listRuns().then(all => setRuns(runsFor(all, namespace))).catch(error => { setRuns([]); toast((error as Error).message, true); }); }, [namespace, toast]);
  if (runs === null) return <p className="empty">Loading runs…</p>;
  if (!runs.length) return <Card variant="sunk" width="full" className="empty-card"><CardHeader title="No runs collected from this sandbox" subtitle="Orchestration runs appear here once the collector publishes their records." /></Card>;
  return <RunTable runs={runs} showHost={false} />;
}
