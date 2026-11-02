// The Files section: download from a URL into the sandbox, or upload a file
// from this computer. Each tab keeps its own source and state.
import {useState} from 'react';
import {Tabs, TabsList, TabsTrigger} from '../../ui';
import type {DownloadSource} from '../sources/downloads';
import type {UploadSource} from '../sources/uploads';
import {Downloads} from './Downloads';
import {Uploads} from './Uploads';

export type FilesTab = 'download' | 'upload';

export function Files({downloads, uploads, workspace}: {downloads: DownloadSource; uploads: UploadSource; workspace: string}) {
  const [tab, setTab] = useState<FilesTab>('download');
  return (
    <div className="stack" data-testid="files">
      <Tabs value={tab} onValueChange={v => setTab(v === 'upload' ? 'upload' : 'download')} className="inner-tabs">
        <TabsList aria-label="Files">
          <TabsTrigger value="download" text="Download from URL" />
          <TabsTrigger value="upload" text="Upload from this computer" />
        </TabsList>
      </Tabs>
      {tab === 'download' ? <Downloads source={downloads} workspace={workspace} /> : <Uploads source={uploads} workspace={workspace} />}
    </div>
  );
}
