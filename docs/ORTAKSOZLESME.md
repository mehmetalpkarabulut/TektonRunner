# Ortak Sozlesme

Bu dokuman, Tekton Runner platformuna deploy edilecek yeni repo/proje ureten tum gelistiriciler icin kurumsal standartlari tanimlar. Amac, farkli ekiplerden gelen projelerin ayni operasyon modeliyle guvenli, izlenebilir ve otomasyon uyumlu sekilde calismasidir.

## 1. Amac ve Kapsam

- Tum uygulamalarda tek tip deploy davranisi saglamak.
- Elle islem ihtiyacini azaltmak, pipeline basarisini artirmak.
- Uygulama, container, veritabani, migration, gozlemlenebilirlik ve guvenlik beklentilerini netlestirmek.

Bu sozlesme .NET tabanli servisler icin zorunlu; diger stackler icin de referans model olarak kullanilir.

## 1.1 Staging Icin Zorunlu Minimum Kurallar

1. Uygulama su env anahtarlarini kullanmali:
- `ConnectionStrings__DefaultConnection`
- `ConnectionStrings__Redis`

2. Baglanti string parse esnek olmalidir:
- PostgreSQL icin hem `Host=...;Port=...` hem URL formati desteklenmeli.
- Redis icin `host:port` gelirse otomatik `redis://` eklenmeli.

3. Staging'de migration adimi pipeline disi tutulur:
- `migration.enabled=false`
- Uygulama startup'ta semayi kendisi hazirlar (`Migrate` / `EnsureCreated` / `CREATE TABLE IF NOT EXISTS`).

4. Tekton dependency secimi:
- DB+cache gereken servislerde `dependency.type=both`.

5. Operasyon:
- Ayni workspace/app icin eszamanli birden fazla tetikleme yapilmaz.

## 2. Zorunlu Repo Icerigi

Her repo asagidakileri icermelidir:

1. `Dockerfile`
- Uretim image'i tek komutla olusturulabilmeli.
- Final image minimum gerekli dosyalari icermeli.

2. `migrate.sh`
- Veritabani migration giris scripti.
- Image icinde `/app/migrate.sh` yoluna kopyalanmali.
- Calistirilabilir olmali (`chmod +x`).

3. `Migrations/` (EF Core kullanan projelerde)
- Migration dosyalari repoda versiyonlanmali.
- "Lokalimde var" tipinde migration kabul edilmez.

4. `README.md`
- Projenin calisma amaci.
- Gerekli environment variable listesi.
- Lokalde calistirma adimlari.
- Migration ve health endpoint bilgisi.

5. `.dockerignore`
- Gereksiz dosyalar image build context'ine girmemeli.

## 3. Uygulama Davranis Sozlesmesi

1. Port
- Uygulama container icinde tek port dinlemeli.
- Varsayilan beklenti: `8080` (farkli ise acikca belirtilmeli).

2. Health endpoint
- En az bir health endpoint olmali (`/health`, `/healthz` veya benzeri).
- Uygulama hazir olmadan basarili donmemeli.

3. Graceful shutdown
- SIGTERM aldiginda duzgun kapanis yapmali.
- Ani kapanista veri kaybi/yarim islem birakmamali.

4. Stateless davranis
- Uygulama container filesystem'ini kalici depolama gibi kullanmamali.
- Kalici veri DB/object storage uzerinden yonetilmeli.

## 4. Configuration ve Secret Yonetimi

1. Konfigurasyonlar env var ile verilmeli.
2. Secret'lar repoya yazilmamali (sifre, token, sertifika vb.).
3. Ortam bazli degerler hard-code edilmemeli.
4. Zorunlu env varlar README'de "required" olarak acik belirtilmeli.

## 5. Veritabani ve Connection String Sozlesmesi

Platform DB baglantisini otomatik uretip secret olarak enjekte eder.

Zorunlu standart:
- Varsayilan DB env anahtari: `ConnectionStrings__DefaultConnection`

Kurallar:
1. Uygulama baglanti bilgisini bu env anahtarindan okuyabilmeli.
2. Connection string kod icinde sabit yazilmamali.
3. SQL dependency seciliyse uygulama PostgreSQL uyumlu olmalidir.

## 6. Migration Sozlesmesi (Zorunlu)

1. `migration.enabled=true` kullanildiginda migration adimi deploy oncesi calisir.
2. `migration.command/args` bos birakilabilir; bu durumda runner image icinde standart script yollarini dener.
3. Minimum beklenti: `/app/migrate.sh` image icinde mevcut ve executable olmali.

### 6.1 Onerilen Model: EfBundle

EF Core projelerinde runtime'da `dotnet-ef` bagimliligini azaltmak icin `efbundle` onerilir.

Ornek akisin ozeti:
1. Build stage'de `dotnet ef migrations bundle` uret.
2. Final stage'e `/app/efbundle` kopyala.
3. `migrate.sh` icinde `efbundle` calistir.

Ornek `migrate.sh`:

```sh
#!/usr/bin/env sh
set -eu

/app/efbundle --connection "$ConnectionStrings__DefaultConnection"
```

## 7. Dockerfile Sozlesmesi

1. Multi-stage build kullanimi onerilir.
2. Final image'ta root disi user tercih edilmelidir.
3. Asagidakiler final image'a alinmalidir:
- uygulama binary/artifact
- `/app/migrate.sh`
- (varsa) `/app/efbundle`

4. Zorunlu izinler:

```dockerfile
COPY migrate.sh /app/migrate.sh
RUN chmod +x /app/migrate.sh
```

5. Image tag stratejisi:
- `latest` yaninda surumlu/tagli image kullanimi zorunlu tavsiyedir.

## 8. Gozlemlenebilirlik ve Loglama

1. Uygulama loglari stdout/stderr uzerinden cikmali.
2. Loglar yapisal (en azindan seviyeli) olmali: `info`, `warn`, `error`.
3. Hata aninda anlasilir mesaj uretmeli; sessiz fail kabul edilmez.
4. Baslangic logunda asagidakiler gorunmeli:
- uygulama surumu
- dinlenen port
- aktif ortam/profil (gizli bilgi icermeden)

## 9. Guvenlik Gereksinimleri

1. Hard-coded secret yasak.
2. Gereksiz acik port yasak.
3. Kullanilmayan package/tool image'tan temizlenmeli.
4. Mumkunse minimal base image kullanilmali.
5. Uygulama icinde admin/default sifre ile calisma yasak.

## 10. CI/CD ve Release Beklentileri

1. Her merge/push'ta en az build dogrulamasi kosmali.
2. Test kapsami ekip hedefine gore artirilir; minimum smoke test onerilir.
3. Uretim release'leri geri alinabilir (rollback-friendly) olmalidir.
4. Kirici degisiklikler README ve release note'da belirtilmelidir.

## 11. Monorepo / Coklu Servis Durumu

Coklu servis barindiran repolarda:
1. Her servis icin acik context sub-path tanimlanmali.
2. Her servisin kendi migration ve config gereksinimi dokumante edilmeli.
3. Ortak scriptler (build/migrate) standart adla versiyonlanmali.

## 12. Minimum Uyum Kontrol Listesi

Deploy oncesi "hazir" sayilmak icin tum maddeler `Evet` olmali:

1. Dockerfile var ve image build aliyor.
2. `migrate.sh` var ve executable.
3. (EF ise) `Migrations/` repoda mevcut.
4. Uygulama `ConnectionStrings__DefaultConnection` okuyabiliyor.
5. Health endpoint calisiyor.
6. Loglar stdout/stderr'e akiyor.
7. Secret'lar repoya yazilmamis.
8. README gerekli env ve calistirma adimlarini iceriyor.

## 13. Platform Uyum Ozeti

Bu sozlesmeye uygun reposu olan ekipler:
- Tekton Runner ile daha az manuel mudahale ile deploy olur,
- DB migration adiminda daha az hata alir,
- Operasyon ve destek surecinde daha hizli sorun cozer.

Sozlesmeye uyumsuzluk durumunda platform ekibi deploy'u durdurma veya duzeltme talebi acma hakkina sahiptir.
