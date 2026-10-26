// A modal confirmation for actions that stop or restart a workspace pod.
// Native <dialog>: modal focus, Escape cancels, and focus starts on Cancel so
// Enter never confirms by accident.
import {createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode} from 'react';
import {Button} from '../ui';

interface Request { title: string; body: ReactNode; confirm: string; resolve: (ok: boolean) => void }
type Ask = (title: string, body: ReactNode, confirm: string) => Promise<boolean>;
const Context = createContext<Ask>(() => Promise.resolve(false));
export const useConfirm = () => useContext(Context);

export function ConfirmProvider({children}: {children: ReactNode}) {
  const [request, setRequest] = useState<Request | null>(null);
  const dialog = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const ask = useCallback<Ask>((title, body, confirm) => new Promise(resolve => setRequest({title, body, confirm, resolve})), []);
  useEffect(() => {
    const d = dialog.current;
    if (!request || !d) return;
    if (!d.open) d.showModal();
    cancel.current?.focus();
  }, [request]);
  const done = (ok: boolean) => {
    dialog.current?.close();
    request?.resolve(ok);
    setRequest(null);
  };
  return (
    <Context.Provider value={ask}>
      {children}
      <dialog ref={dialog} className="confirm-dialog" aria-labelledby="confirm-title" aria-describedby="confirm-body" onCancel={event => { event.preventDefault(); done(false); }}>
        {request ? <>
          <h2 id="confirm-title">{request.title}</h2>
          <div id="confirm-body" className="confirm-body">{request.body}</div>
          <div className="confirm-actions">
            <Button ref={cancel} variant="secondary" onClick={() => done(false)}>Cancel</Button>
            <Button variant="primary" buttonType="destructive" onClick={() => done(true)}>{request.confirm}</Button>
          </div>
        </> : null}
      </dialog>
    </Context.Provider>
  );
}
