# Platform Layout

Bu ortamda Tekton Runner dizinleri tek kaynak olacak sekilde standardize edildi.

## Canonical Source
- Repo koku: `/home/beko/TektonRunner`
- Runner kodu: `/home/beko/TektonRunner/tools/tekton-runner`

## Runtime
- Systemd service: `tekton-runner.service`
- Runtime path: `/home/beko/tools/tekton-runner`
- Bu path symlink olarak tutulur:
  - `/home/beko/tools/tekton-runner -> /home/beko/TektonRunner/tools/tekton-runner`

Bu modelin amaci kod drift'ini engellemektir. Kod degisiklikleri her zaman repo path'inde yapilir.

## Legacy Copies
- Eski canli kopyalar silinmez, `/home/beko/archive` altina timestamp ile tasinir.

## Logs
- Timeline: `/home/beko/run-events.jsonl`
- Ham log arsivi: `/home/beko/run-log-archive`

## Harbor
- Harbor root: `/home/beko/harbor`
- Harbor installer: `/home/beko/harbor/harbor`
