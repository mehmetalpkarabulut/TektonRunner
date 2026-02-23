# AGENTS.md

Bu repo icin Codex calisma kurali:

1. Ilk olarak `CODEX_BOOTSTRAP.md` dosyasini oku.
2. Ortami ayaga kaldirma, dogrulama ve troubleshooting adimlarini oradaki siraya gore uygula.
3. Varsayilan host adini `lenovo`, varsayilan Harbor adresini `https://lenovo:8443` kabul et.
4. Host IP'si degismis olabilir. Komutlarda `<HOST_IP>` placeholder kullan ve gercek IP'yi runtime'da tespit et.
5. Degisiklik yaptiktan sonra:
   - ilgili dokumani guncelle,
   - commit at,
   - `origin/main` dalina push et.
6. Workspace node'larda image pull sorunu varsa `CODEX_BOOTSTRAP.md` icindeki `lenovo no such host` fix adimlarini uygula.

Not: Bu repo test/lab ortam odaklidir; uretim hardening adimlari ayrica planlanmalidir.
