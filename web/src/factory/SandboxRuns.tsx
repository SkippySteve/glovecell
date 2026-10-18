// The Runs tab on a sandbox page: orchestration runs collected from that sandbox.
import {useEffect, useState} from 'react';
import {useData} from '../data';
import {listRuns, type RunSummary} from './api';
import {runHref} from './href';
import {Card, CardHeader, Table, TableBody, TableCell, TableHead, TableHeader, TableRow} from './ui';

// A run's host is "<namespace>/<pod>", as the collector records it.
export const runsFor = (runs: RunSummary[], namespace: string) => runs.filter(run => run.host.split('/')[0] === namespace);

export function SandboxRuns({namespace}: {namespace: string}) {
  const {toast} = useData();
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  useEffect(() => { listRuns().then(all => setRuns(runsFor(all, namespace))).catch(error => { setRuns([]); toast((error as Error).message, true); }); }, [namespace, toast]);
  if (runs === null) return <p className="empty">Loading runs…</p>;
  if (!runs.length) return <Card variant="sunk" width="full" className="empty-card"><CardHeader title="No runs collected from this sandbox" subtitle="Orchestration runs appear here once the collector publishes their records." /></Card>;
  return (
    <Table>
      <TableHeader><TableRow><TableHead>Run</TableHead><TableHead>Objective</TableHead><TableHead>Tasks</TableHead><TableHead>Collected</TableHead></TableRow></TableHeader>
      <TableBody>{runs.map(run => (
        <TableRow key={run.id}>
          <TableCell><a className="row-title" href={runHref(run.id)}>{run.id}</a></TableCell>
          <TableCell>{run.objective}</TableCell>
          <TableCell>{run.tasks}</TableCell>
          <TableCell>{new Date(run.collectedAt).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'})}</TableCell>
        </TableRow>
      ))}</TableBody>
    </Table>
  );
}
