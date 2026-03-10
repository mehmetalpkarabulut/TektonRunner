# Ortak Sozlesme (Tum Diller)

Bu dokuman, Tekton Runner platformuna deploy edilen tum uygulamalar icin ortak konfigurasyon ve deployment sozlesmesidir.

Kapsam:
- .NET, Java, Node.js, Python, Go
- Tek app veya multi-app (`apps[]`) deploy
- Ayni workspace icinde birden fazla pod/service

## 1. Temel Prensip

1. Uygulama kodu ortaktan gelsin, ortam farklari runtime env ile verilsin.
2. Secret ve connection string degerleri repoda tutulmasin.
3. Uygulama config dosyasi (`appsettings`, `application.yml`, `.env.example`) sadece key/placeholder tasiyabilir.
4. Gercek degeri runner Kubernetes Deployment `env` alanina inject eder.

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

Bu anahtarlar .NET disi dillerde dogrudan kullanilmayabilir; yine de platform standardi olarak tutulur.

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
- `ConnectionStrings__Hangfire` anahtari SQL olarak kabul edilir ve SQL connection ile override edilir.

## 5. Connection String Formatlari

### 5.1 SQL (.NET/Npgsql formati)

`Host=postgres.<workspace>.svc.cluster.local;Port=5432;Database=<DB>;Username=postgres;Password=<PASS>;SSL Mode=Disable;`

### 5.2 SQL (URL formati)

`postgresql://postgres:<PASS>@postgres.<workspace>.svc.cluster.local:5432/<DB>?sslmode=disable`

### 5.3 Redis

- Host/port: `redis.<workspace>.svc.cluster.local:6379`
- URL: `redis://redis.<workspace>.svc.cluster.local:6379/0`

## 6. Dil Bazli Uygulama Beklentisi

### 6.1 .NET

`appsettings*.json` icinde su keyler olmali:

```json
{
  "ConnectionStrings": {
    "DefaultConnection": "placeholder",
    "Redis": "placeholder",
    "Hangfire": "placeholder"
  }
}
```

Runtime'da `ConnectionStrings__...` env ile override edilir.

### 6.2 Java (Spring Boot)

Onerilen binding:
- `spring.datasource.url=${DATABASE_URL}`
- `spring.data.redis.url=${REDIS_URL}`

### 6.3 Node.js

- `process.env.DATABASE_URL`
- `process.env.REDIS_URL`

### 6.4 Python

- `os.environ["DATABASE_URL"]`
- `os.environ["REDIS_URL"]`

### 6.5 Go

- `os.Getenv("DATABASE_URL")`
- `os.Getenv("REDIS_URL")`

## 7. runtime_profile Kullanimi

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

## 8. extra_env Kurali

- `extra_env` her zaman opsiyoneldir.
- Connection tipi bir key (`ConnectionStrings__...`, `DATABASE_URL`, `REDIS_URL`, vb.) `extra_env` ile gelirse platformun otomatik degerini override eder.

## 8.1 Placeholder Replacements Kurali

Runner isteginde su alanlar kullanilir:

- `auto_defaults` (varsayilan: `true`)
- `replacements` (kullanici map'i)

Platform varsayilan placeholder'lari:

- `{default_db}` -> SQL .NET connection string
- `{default_db_url}` -> SQL URL
- `{default_redis}` -> `host:port`
- `{default_redis_url}` -> Redis URL

Kullanici ek placeholder verebilir:

```json
{
  "replacements": {
    "{Mehmet}": "x"
  }
}
```

Runner `appsettings*.json` icindeki string alanlari tarar, placeholder replace eder ve degisen key-path'leri Kubernetes env olarak inject eder.

### 8.2 Uygulama ve UI Ornekleri

Appsettings icinde placeholder girisi:

```json
{
  "ConnectionStrings": {
    "DefaultConnection": "{default_db}",
    "Redis": "{default_redis_url}",
    "MasterDataConnection": "{Mehmet}"
  },
  "FeatureFlags": {
    "TenantCode": "{TenantCode}"
  }
}
```

UI `Placeholder Replacements` alanina giris formati:

```text
{Mehmet}=Host=postgres.ws-demo.svc.cluster.local;Port=5432;Database=MasterDb;Username=postgres;Password=StrongPass_123!;SSL Mode=Disable;
{TenantCode}=TR01
```

Format kurali:
- Her satir `{Key}=value` seklindedir.
- Sol taraf mutlaka `{}` ile yazilmalidir.
- DB icin standart token: `{default_db}`
- Redis URL icin standart token: `{default_redis_url}`

## 9. Appsettings Tarama Mantigi

Runner kaynak kodu (`git` veya `zip`) indirir, `appsettings*.json` dosyalarinda `ConnectionStrings` keylerini tarar ve uygun env maplerini olusturur.

Not:
- Key placeholder olsa bile platform key adina gore map uretir.
- `Hangfire` keyleri SQL kabul edilir.

## 10. Multi-App Ornek Request

```json
{
  "workspace": "ws-suite",
  "runtime_profile": "auto",
  "auto_defaults": true,
  "replacements": {
    "{Mehmet}": "x"
  },
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

## 11. Uygulama Ekiplerine Verilecek Kisa Sozlesme

Platform ekibi uygulama ekiplerine su 4 maddeyi vermelidir:

1. Config dosyaniza `ConnectionStrings.DefaultConnection`, `ConnectionStrings.Redis`, (varsa) `ConnectionStrings.Hangfire` keylerini ekleyin.
2. Bu degerler placeholder olabilir; gercek degeri platform runtime'da verecek.
3. Uygulamaniz env override okuyacak sekilde yazilmis olmali.
4. Ozel key gerekiyorsa platforma `extra_env` ile bildirin.

## 12. Ne Zaman Hata Alinir?

Tipik nedenler:
- Uygulama placeholder'i runtime env ile override edemiyor.
- Uygulama farkli key bekliyor (ornegin `MyDbConnection`) ama platforma bildirilmemis.
- Yanlis runtime profile secimi.
- Build/push asamasinda registry/network hatasi.

## 13. Sonuc

Bu sozlesme ile:
- Dil fark etmeksizin tek deploy modeli korunur.
- Ayni workspace icinde coklu pod senaryosu standardize edilir.
- Connection string ve secret yonetimi merkezi ve izlenebilir kalir.
