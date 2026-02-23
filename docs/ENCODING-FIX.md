# Encoding Fix Script

Projedeki `ISO-8859-9`, `CP1254`, bozuk UTF-8 ve double-encoded Turkce karakterleri temizlemek icin:

```bash
cd /home/beko/TektonRunner
chmod +x scripts/fix-encoding.sh
scripts/fix-encoding.sh . --dry-run
scripts/fix-encoding.sh .
```

Opsiyonlar:
- `--dry-run`: Degisiklik yapmadan raporlar
- `--no-backup`: `.bak-encoding` yedegi olusturmaz

Not:
- Script metin dosyalarina odaklanir.
- Varsayilan olarak `.git`, `.cache`, `.codex` ve `harbor/data` dislanir.
