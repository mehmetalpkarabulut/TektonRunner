# tekton-runner (Go)

Bu araç, uygulamadan gelen JSON isteğini Tekton manifestlerine çevirir ve isteğe bağlı olarak `kubectl apply` yapar.

## Build

```bash
go build -o tekton-runner ./...
```

## Kullanım

### STDIN -> stdout

```bash
cat request.json | ./tekton-runner
```

### Dosyadan al, çıktı dizinine yaz

```bash
./tekton-runner -in request.json -out-dir /tmp/tekton
```

### Uygula

```bash
./tekton-runner -in request.json -apply
```

## JSON Şema

```json
{
  "namespace": "tekton-pipelines",
  "task": "build-and-push-generic",
  "source": {
    "type": "git|zip",
    "repo_url": "https://github.com/user/repo",
    "revision": "main",
    "git_username": "user",
    "git_token": "token",
    "git_secret": "optional-secret-name",
    "zip_url": "https://example.com/source.zip",
    "zip_username": "user",
    "zip_password": "pass"
  },
  "image": {
    "project": "myapp",
    "tag": "latest",
    "registry": "lenovo:8443"
  },
  "file_storage": {
    "enabled": true,
    "pvc_name": "shared-file-pvc",
    "mount_path": "/app/storage",
    "sub_path": "myapp",
    "nfs": {
      "server": "10.0.0.10",
      "path": "/exports/shared",
      "size": "1Gi"
    }
  }
}
```

## Notlar

- `source.type=git` için `repo_url` zorunlu.
- Git kullanıcı/şifre verilirse secret otomatik oluşturulur.
- Harbor project adı workspace'ten üretilir. `ws-myapp` workspace'i Harbor'da `myapp` projesine yazar.
- `image.project` ve `apps[].project` Harbor project adı degil, proje icindeki repo adidir. Bos ise app adi kullanilir.
- NFS/SMB bilgisi verilirse PV+PVC (ve SMB secret) otomatik oluşturulur.
- `file_storage.enabled=true` verilirse uygulama pod'una ortak RWX storage mount edilir.
- `file_storage.sub_path` bos birakilirsa runner NFS/SMB paylasimi altinda otomatik klasor olusturur. Tek app deploy'da varsayilan klasor workspace adinin `ws-` siz halidir; multi-app deploy'da `<workspace>/<app>` formatina duser.
- `file_storage.nfs` veya `file_storage.smb` verilirse workspace cluster icinde ilgili PVC otomatik olusturulur.
- `file_storage.enabled=true` ve storage backend bilgisi verilmezse varsayilan olarak `10.134.70.112:/srv/nfs/shared` ve `1Gi` kullanilir.

## SMB Notu

SMB kullanımı için cluster'da SMB CSI driver bulunmalıdır. Yoksa PV/PVC oluşturulsa bile mount başarısız olur.

## Gerekli Tekton Önkoşulları

- `build-and-push-generic` Task kurulu olmalı.
- `build-bot` ServiceAccount ve `harbor-creds` Secret hazır olmalı.
- NFS/SMB ile PV oluşturulacaksa ClusterRole gerekir. Örnek: `manifests/tekton-runner-rbac.yaml`.

## HTTP Sunucu Modu

### Çalıştırma

```bash
./tekton-runner -server -addr :8088
```

API key ile:

```bash
./tekton-runner -server -addr :8088 -api-key YOUR_KEY
```

### Endpointler

- `GET /healthz` -> `ok`
- `POST /zip/upload` -> multipart ile zip yukler, `zip-server:/srv/<file>.zip` olarak kaydeder
- `DELETE /zip/delete?filename=<file>.zip` -> `zip-server:/srv/<file>.zip` dosyasini siler
- `POST /run` -> JSON alır, manifestleri apply eder
- `POST /run?dry_run=true` -> YAML döner
- `GET /run/logs?workspace=ws-...&app=...` -> build+deploy timeline loglari (JSON)
- `GET /run/logs?workspace=ws-...&app=...&format=text` -> okunabilir timeline loglari (text)

`/run/logs` varsayilan olarak su loglari bir arada dondurur:
- timeline event loglari
- ilgili TaskRun ham loglari
- workspace pod/container ham loglari

Kalici log arsivi:
- timeline eventleri: `/home/beko/run-events.jsonl`
- TaskRun ham log arsivi: `/home/beko/run-log-archive/<workspace>/<app>/taskrun/*.json`
- container ham log arsivi: `/home/beko/run-log-archive/<workspace>/<app>/container/*.json`

Davranis:
- Canli log basarili okunursa arsive yazilir (`source=live`).
- Canli log okunamazsa varsa arsivden donulur (`source=archive`).
- `run_id` vermeseniz de `workspace+app` ile gecmis loglar okunabilir.

Opsiyonel query parametreleri:
- `include_taskrun=true|false`
- `include_containers=true|false`
- `tail_raw=400` (TaskRun/container ham log tail satiri)

### Postman Örneği

- Method: `POST`
- URL: `http://<host>:8088/run`
- Header: `Content-Type: application/json`
- Header (opsiyonel): `Authorization: Bearer YOUR_KEY`
- Body: raw JSON (şema README üst kısmında)

`POST /run` cevabinda `run_id` doner. Bu id ile log sorgusu daraltilabilir:

```bash
curl -s "http://<host>:8088/run/logs?workspace=ws-demo&app=demoapp&run_id=<RUN_ID>&format=text"
```

### Zip Upload Ornegi

`zip-server` icine yeni zip dosyasi yuklemek icin:

```bash
curl -sS -X POST "http://<host>:8088/zip/upload" \
  -H "Authorization: Bearer YOUR_KEY" \
  -F "file=@/home/beko/myproject.zip"
```

Opsiyonel dosya adi override:

```bash
curl -sS -X POST "http://<host>:8088/zip/upload" \
  -H "Authorization: Bearer YOUR_KEY" \
  -F "file=@/home/beko/myproject.zip" \
  -F "filename=demo-v2.zip"
```

### Zip Delete Ornegi

Yuklenen zip dosyasini silmek icin:

```bash
curl -sS -X DELETE "http://<host>:8088/zip/delete?filename=demo-v2.zip" \
  -H "Authorization: Bearer YOUR_KEY"
```

Alternatif olarak `POST` ile de cagrilabilir:

```bash
curl -sS -X POST "http://<host>:8088/zip/delete" \
  -H "Authorization: Bearer YOUR_KEY" \
  -d "filename=demo-v2.zip"
```

### Multi-App (tek run, birden fazla pod)

`apps[]` ile ayni kaynak icinden birden fazla uygulama build+deploy edilebilir.
Her app icin ayri TaskRun olusur, deploy asamasinda her biri ayri Deployment/Service (ayri pod) olarak ayaga kalkar.

Ornek:

```json
{
  "workspace": "ws-suite",
  "source": {
    "type": "zip",
    "zip_url": "http://10.0.0.10/my-suite.zip"
  },
  "image": {
    "registry": "lenovo:8443",
    "tag": "latest"
  },
  "apps": [
    {
      "app_name": "memo-api",
      "project": "memo-api",
      "container_port": 8080,
      "context_sub_path": "backend"
    },
    {
      "app_name": "memo-ui",
      "project": "memo-ui",
      "container_port": 8501,
      "context_sub_path": "ui"
    }
  ]
}
```

Notlar:
- `apps[]` verildiginde `app_name` ve `image.project` zorunlu degildir.
- `apps[].container_port` opsiyoneldir. Verilmezse runner once Dockerfile `EXPOSE` bilgisini kullanir, bulamazsa varsayilan porta duser.
- `apps[].project` bossa `apps[].app_name` kullanilir.
- Harbor project adi `workspace` alanindan uretilir. Ornek: `ws-suite` -> Harbor project `suite`.
- Bu durumda ornekteki image yolları `lenovo:8443/suite/memo-api:latest` ve `lenovo:8443/suite/memo-ui:latest` olur.
- `apps[].tag` bossa `image.tag` (yoksa `latest`) kullanilir.
- `context_sub_path` verilirse build context o alt klasor olur.
- `container_port` verilirse uygulamanin gercek dinledigi port olmalidir. Ornek: FastAPI/Uvicorn cogu zaman `8000`, Streamlit `8501`.
- `source.type=zip` icin `apps[]` ve `source.context_sub_path` verilmezse, ZIP icindeki tum Dockerfile konumlari otomatik taranir ve her Dockerfile icin otomatik app olusturulur (multi-app run).
- Deploy sonrasi pod loglari taranir; uygulama logu farkli bir listening port yaziyorsa run `deploy failed` olur.
- Deploy edilen tum app'lere `PORT` env'i de otomatik yazilir.

### Multi-App Python Ornegi (FastAPI + Streamlit)

```json
{
  "workspace": "ws-python",
  "source": {
    "type": "zip",
    "zip_url": "http://zip-server.tekton-pipelines.svc.cluster.local:8080/python-suite.zip"
  },
  "image": {
    "registry": "lenovo:8443",
    "tag": "latest"
  },
  "dependency": {
    "type": "both",
    "redis": {
      "port": 6379
    },
    "sql": {
      "port": 5432,
      "database": "AppDb",
      "username": "postgres",
      "password": "StrongPass_123!"
    }
  },
  "apps": [
    {
      "app_name": "backend",
      "project": "backend",
      "container_port": 8000,
      "context_sub_path": "backend"
    },
    {
      "app_name": "ui",
      "project": "ui",
      "container_port": 8501,
      "context_sub_path": "ui"
    }
  ]
}
```

Bu tip bir pakette runner otomatik olarak frontend app'lere asagidaki alias env'leri enjekte eder:
- `BACKEND_BASE_URL`
- `BACKEND_URL`
- `API_BASE_URL`
- `VITE_API_BASE_URL`
- `NEXT_PUBLIC_API_URL`

## Dependency Orchestration (Redis / SQL)

`/run` isteginde `dependency` alani ile workspace icinde ek servis ayaga kaldirilabilir.
Bu ozellik `source.type=git|zip|local` akislari icin ayni sekilde desteklenir.

Akis:
1. Workspace kind cluster hazirlanir.
2. `dependency.type` secimine gore Redis veya SQL Deployment+Service kurulur.
3. Dependency `Ready` olana kadar beklenir.
4. Connection string `Secret` icine yazilir.
5. Uygulama Deployment'i Secret'tan env override ile ayaga kalkar.

Not: `appsettings.json` fiziksel degistirilmez. .NET tarafinda env (`ConnectionStrings__...`) appsettings degerini runtime'da override eder.

Runner ayni anda ortak env'leri de uretir:
- `DATABASE_URL`
- `REDIS_URL`
- `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `DB_SSLMODE`
- `REDIS_HOST`, `REDIS_PORT`

Boylece .NET, Python, Node.js ve benzeri farkli uygulamalar ayni dependency akisini kullanabilir.

### Dependency JSON Ornegi (Redis)

```json
{
  "app_name": "memo-app",
  "source": {
    "type": "git",
    "repo_url": "https://github.com/org/repo",
    "revision": "main"
  },
  "image": {
    "project": "memo-app",
    "tag": "latest",
    "registry": "lenovo:8443"
  },
  "dependency": {
    "type": "redis",
    "redis": {
      "image": "lenovo:8443/library/redis:7-alpine",
      "service_name": "redis",
      "port": 6379,
      "connection_env": "ConnectionStrings__Redis"
    }
  }
}
```

### Dependency JSON Ornegi (SQL Server)

```json
{
  "app_name": "memo-api",
  "source": {
    "type": "git",
    "repo_url": "https://github.com/org/repo",
    "revision": "main"
  },
  "image": {
    "project": "memo-api",
    "tag": "latest",
    "registry": "lenovo:8443"
  },
  "dependency": {
    "type": "sql",
    "sql": {
      "image": "lenovo:8443/library/mssql-server:2022-latest",
      "service_name": "sql",
      "port": 1433,
      "database": "MemoDb",
      "username": "sa",
      "password": "StrongPass_123!",
      "connection_env": "ConnectionStrings__DefaultConnection"
    }
  }
}
```

## Harbor Mirror (Redis / SQL)

Dependency image'lerinin internetten cekilmemesi icin bir kez hosttan Harbor'a push edin:

```bash
sudo docker pull redis:7-alpine
sudo docker tag redis:7-alpine lenovo:8443/library/redis:7-alpine
sudo docker push lenovo:8443/library/redis:7-alpine

sudo docker pull mcr.microsoft.com/mssql/server:2022-latest
sudo docker tag mcr.microsoft.com/mssql/server:2022-latest lenovo:8443/library/mssql-server:2022-latest
sudo docker push lenovo:8443/library/mssql-server:2022-latest
```

## DB Migration Job (Opsiyonel)

SQL dependency kullanirken uygulama podundan once migration job calistirilabilir.

```json
"migration": {
  "enabled": true,
  "image": "lenovo:8443/library/dotnet-sdk:8.0",
  "command": ["dotnet", "ef", "database", "update"],
  "args": [],
  "env_name": "ConnectionStrings__DefaultConnection"
}
```

Notlar:
- `migration.enabled=true` icin `dependency.type=sql` zorunludur.
- Job, `env_name` anahtarini app icin uretilen Secret'tan alir.
- Migration basarisizsa app deploy edilmez.

## SQL External Access Notes

- SQL dependency HTTP endpoint vermez; external URL TCP icindir.
- Tarayicidan degil, SQL client ile baglanin (DBeaver/sqlcmd).
- DBeaver:
  - Driver: `SQL Server (Microsoft)`
  - Auth: `SQL Server Authentication`
  - SSL: `encrypt=false` veya `trustServerCertificate=true`

Pre-login hatasi gorulurse `tekton-runner` restart edilip external forward'in guncel NodePort'a baglandigi kontrol edilmelidir.

### SQL Default Resource Guardrails

SQL dependency olusurken varsayilan asagidaki limitler uygulanir:

- `MSSQL_MEMORY_LIMIT_MB=1024`
- `requests`: `cpu=250m`, `memory=512Mi`
- `limits`: `cpu=1`, `memory=1536Mi`

Amac: workspace node'da SQL'in kontrolsuz bellek tuketmesini engellemek.

## 2026-02-18 Update Summary

Bu tarihte eklenenler:
- `dependency.type` destegi: `none|redis|sql|both`
- SQL icin opsiyonel migration job (`migration.enabled`)
- Secret/env override modeli (appsettings fiziksel replace yok)
- SQL default resource guardrails:
  - `MSSQL_MEMORY_LIMIT_MB=1024`
  - requests: `250m / 512Mi`
  - limits: `1 CPU / 1536Mi`
- SQL external access notlari (DBeaver pre-login/login failed troubleshooting)

## 2026-02-23 Update Summary

Bu tarihte eklenenler:
- ZIP Build UI formuna dependency secimi eklendi (`none|redis|sql|both`)
- ZIP/Local formlarina SQL zorunlu alanlari eklendi (`database`, `password`)
- ZIP/Local icin advanced dependency override alanlari eklendi (redis/sql image, service, port, env)
- CLI `-apply` akisinda deploy/dependency orchestration `git|zip|local` icin ayni sekilde tetiklenir
