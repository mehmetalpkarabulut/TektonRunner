# TektonRunner

Tekton tabanli build + Harbor push + workspace deploy orchestration reposu.

## Baslangic

- Codex icin ilk okunacak: `AGENTS.md`
- Ortam kurulum/runbook: `CODEX_BOOTSTRAP.md`
- Uctan uca akis (kim ne yapar): `docs/tekton-runner-end-to-end-flow.md`
- Proje ureten ekipler icin ortak gereksinimler: `docs/ORTAKSOZLESME.md`
- Connection string sozlesmesi ve runtime mapping notlari: `tools/tekton-runner/README.md`

## Ana Klasorler

- `tools/tekton-runner/` -> runner kodu + UI
- `manifests/` -> Tekton task/RBAC
- `docs/` -> operasyon ve setup dokumanlari
