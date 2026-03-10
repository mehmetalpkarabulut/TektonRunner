# Ortak Sozlesme (Tum Diller)

Bu dokuman, Tekton Runner platformuna deploy edilen tum uygulamalar icin ortak konfigurasyon ve deployment sozlesmesidir.

Kapsam:
- .NET, Java, Node.js, Python, Go
- Tek app veya multi-app (`apps[]`) deploy
- Ayni workspace icinde birden fazla pod/service

## 1. Temel Prensip

1. Uygulama kodu ortaktan gelsin, ortam farklari runtime env ile verilsin.
2. Secret ve connection string degerleri repoda tutulmasin.
3. Uygulama config dosyasi (`appsettings`, `application.yml`, `.env.example`) sadece key/placeholder tasir.
4. Gercek degerleri Tekton Runner Kubernetes Deployment `env` alanina inject eder.
5. Runner `appsettings.json` dosyasini fiziksel olarak degistirmez; env override uygular.

## 2. Workspace ve Pod Modeli

- Her deploy bir workspace cluster/namespace uzerinde calisir (`ws-...`).
- `apps[]` kullanildiginda her app icin ayri `Deployment + Service` olusur.
- Backend ve frontend ayni workspace'te farkli podlarda kosabilir.

Ornek ic DNS:
- `backend.ws-demo.svc.cluster.local`
- `frontend.ws-demo.svc.cluster.local`
- `postgres.ws-demo.svc.cluster.local`
- `redis.ws-demo.svc.cluster.local`

Not:
- Pod portlari ayni olabilir; podlar birbirinden izoledir.
- Service `targetPort` ilgili app'in `container_port` degerine baglanir.

## 3. Zorunlu Config Anahtarlari (Uygulama Tarafi)

Tum ekipler uygulama config dosyasinda asagidaki keyleri tanimlamalidir (placeholder olabilir):

- `ConnectionStrings:DefaultConnection`
- `ConnectionStrings:Redis`
- `ConnectionStrings:Hangfire` (Hangfire kullananlarda)

.NET disi dillerde bu key'ler dogrudan kullanilmayabilir; yine de platform standardi olarak tavsiye edilir.

## 4. Platformun Urettigi Env Anahtarlari

Runner dependency tipine gore env inject eder.

SQL:
- `ConnectionStrings__DefaultConnection`
- `SQL_CONNECTION_STRING`
- `DEFAULT_CONNECTION`
- `DATABASE_URL` (runtime profile'a gore)

Redis:
- `ConnectionStrings__Redis`
- `REDIS_CONNECTION`
- `REDIS_CONNECTION_STRING`
- `REDIS_URL`

Hangfire:
- `ConnectionStrings__Hangfire*` keyleri SQL olarak kabul edilir ve SQL connection ile override edilir.

## 5. Varsayilan Tokenlar ve Formatlar

### 5.1 SQL tokenlari

- `{default_db}` -> .NET/Npgsql string
  - `Host=postgres.<workspace>.svc.cluster.local;Port=5432;Database=<DB>;Username=postgres;Password=<PASS>;SSL Mode=Disable;`
- `{default_db_url}` -> URL formati
  - `postgresql://postgres:<PASS>@postgres.<workspace>.svc.cluster.local:5432/<DB>?sslmode=disable`

### 5.2 Redis tokenlari

- `{default_redis}` -> `host:port`
  - `redis.<workspace>.svc.cluster.local:6379`
- `{default_redis_url}` -> URL
  - `redis://redis.<workspace>.svc.cluster.local:6379/0`

## 6. Appsettings Placeholder Kurali

Desteklenen placeholder formatlari:
- `{token}`
- `#{token}#`

Runner iki formati da replace eder.

Ornek:

```json
{
  "ConnectionStrings": {
    "DefaultConnection": "{default_db}",
    "Redis": "{default_redis}",
    "HangfireConnection": "{default_db}"
  },
  "Cache": {
    "Redis": {
      "ConnectionString": "{default_redis}"
    }
  },
  "Okta": {
    "OktaDomain": "#{oktaDomain}#",
    "AuthorizationServerId": "#{authorizationServerId}#",
    "Audience": "#{oktaAudience}#",
    "ClientId": "#{oktaClientId}#"
  }
}
```

## 7. UI Placeholder Replacements Kurali

UI `Placeholder Replacements` alani satir formati:

```text
{oktaDomain}=https://org.okta.com
{authorizationServerId}=ausxxxxxxxx
{oktaAudience}=api://default
{oktaClientId}=0oaxxxxx
```

Kurallar:
- Her satir `{Key}=value`
- Sol taraf sadece `{}` formunda yazilir (`#{}` yazilmaz)
- Bos key kabul edilmez

## 8. En Kritik Nokta: Redis Token Secimi

Ayni `redis` icin iki token vardir. Dogru token uygulamanin bekledigi formata gore secilir.

- `.NET + StackExchange.Redis` (`AddStackExchangeRedisCache`, `ConnectionMultiplexer`) icin oncelik:
  - `host:port` -> `{default_redis}`
- URL zorunlu bekleyen framework/library icin:
  - `redis://...` -> `{default_redis_url}`

Pratik kural:
- Emin degilsen .NET app'lerde `Cache:Redis:ConnectionString` icin `{default_redis}` kullan.
- URL formatina ihtiyac oldugu kesin degilse `{default_redis_url}` kullanma.

## 9. runtime_profile Kullanimi

Runner isteginde:
- Global: `runtime_profile`
- App bazli: `apps[].runtime_profile`

Desteklenenler:
- `auto` (varsayilan)
- `dotnet`
- `node`
- `python`
- `go`
- `java`
- `custom`

Davranis:
- `dotnet`: `ConnectionStrings__...` aliaslarini one cikarir.
- `node/python/go/java`: `DATABASE_URL` + `REDIS_URL` aliaslarini one cikarir.
- `custom`: sadece explicit/extra env mantigi.

## 10. extra_env Kurali

- `extra_env` opsiyoneldir.
- Connection tipi bir key (`ConnectionStrings__...`, `DATABASE_URL`, `REDIS_URL`, vb.) `extra_env` ile gelirse platformun otomatik degerini override eder.

## 11. Appsettings Tarama Mantigi

Runner kaynak kodu (`git` veya `zip`) indirir, `appsettings*.json` dosyalarini tarar.

Tarama sonucu:
- `ConnectionStrings` altindaki SQL/Redis key'leri map edilir.
- Placeholder replace edilen string key-path'ler env'e cevrilir (`Section__Sub__Key`).
- `Hangfire` gecen `ConnectionStrings` keyleri SQL sayilir.

Not:
- Key placeholder olsa bile platform key adina gore map uretir.
- Replace edilmeyen tokenlar warning olarak run event'e yazilir.

## 12. Deploy Sonrasi Env Guncelleme (Canli Patch)

Deploy olduktan sonra env eklemek/guncellemek icin:
- Endpoint: `POST /app/env`

Request ornegi:

```json
{
  "workspace": "ws-demo",
  "app": "demoapp",
  "restart": true,
  "env": [
    { "name": "ASPNETCORE_ENVIRONMENT", "value": "Staging" },
    { "name": "Okta__OktaDomain", "value": "https://org.okta.com" }
  ]
}
```

Not:
- Bu islem Git repo dosyasini degistirmez; sadece canli deployment env'ini gunceller.
- `restart=true` ile rollout restart tetiklenir.

## 13. Multi-App Ornek Request

```json
{
  "workspace": "ws-suite",
  "runtime_profile": "auto",
  "auto_defaults": true,
  "source": {
    "type": "zip",
    "zip_url": "http://zip-server.tekton-pipelines.svc.cluster.local:8080/suite.zip"
  },
  "image": {
    "registry": "lenovo:8443",
    "tag": "latest"
  },
  "dependency": {
    "type": "both"
  },
  "apps": [
    {
      "app_name": "backend",
      "project": "backend",
      "container_port": 8080,
      "runtime_profile": "dotnet",
      "context_sub_path": "src/backend"
    },
    {
      "app_name": "frontend",
      "project": "frontend",
      "container_port": 3000,
      "runtime_profile": "node",
      "context_sub_path": "src/frontend"
    }
  ]
}
```

## 14. Uygulama Ekiplerine Verilecek Kisa Sozlesme

Platform ekibi uygulama ekiplerine su 6 maddeyi vermelidir:

1. Config dosyaniza `ConnectionStrings.DefaultConnection`, `ConnectionStrings.Redis`, (varsa) `ConnectionStrings.Hangfire` keylerini ekleyin.
2. Bu degerler placeholder olabilir; gercek degerleri platform runtime'da verir.
3. .NET app'lerde Redis icin once `{default_redis}` deneyin.
4. URL gerekiyorsa acikca `{default_redis_url}` kullanin.
5. Ozel key gerekiyorsa `replacements` veya `extra_env` ile bildirin.
6. Deploy sonrasi canli env guncellemesi icin `/app/env` kullanilabilir.

## 15. Sik Hata Nedenleri

- Placeholder yazimi dogru ama uygulama farkli key okuyor.
- `default_redis_url` kullanildi ama uygulama `host:port` bekliyor.
- .NET env key yazim hatasi var (ornek: `ASPNETCORE_ENVIRONMENT` yerine yanlis yazim).
- Uygulama kodu belirli bir ayari (ornek `RequireHttpsMetadata`) config'ten okumuyor.
- Yanlis runtime profile secimi.

## 16. Sonuc

Bu sozlesme ile:
- Dil fark etmeksizin tek deploy modeli korunur.
- Ayni workspace icinde coklu pod senaryosu standardize edilir.
- Connection string ve secret yonetimi merkezi ve izlenebilir kalir.
