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
    "type": "git|local|zip",
    "repo_url": "https://github.com/user/repo",
    "revision": "main",
    "git_username": "user",
    "git_token": "token",
    "git_secret": "optional-secret-name",
    "local_path": "projeler/myapp",
    "pvc_name": "pvc-nfs-123",
    "zip_url": "https://example.com/source.zip",
    "zip_username": "user",
    "zip_password": "pass",
    "nfs": {
      "server": "10.0.0.10",
      "path": "/exports/projects",
      "size": "50Gi"
    },
    "smb": {
      "server": "fileserver.local",
      "share": "projects",
      "username": "user",
      "password": "pass",
      "size": "50Gi",
      "volume_handle": "optional-handle",
      "secret_name": "optional-secret"
    }
  },
  "image": {
    "project": "myapp",
    "tag": "latest",
    "registry": "lenovo:8443"
  }
}
```

## Notlar

- `source.type=git` için `repo_url` zorunlu.
- `source.type=local` için `local_path` zorunlu ve `pvc_name` ya da `nfs/smb` zorunlu.
- Git kullanıcı/şifre verilirse secret otomatik oluşturulur.
- NFS/SMB bilgisi verilirse PV+PVC (ve SMB secret) otomatik oluşturulur.

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
- `POST /run` -> JSON alır, manifestleri apply eder
- `POST /run?dry_run=true` -> YAML döner
- `GET /run/logs?workspace=ws-...&app=...` -> build+deploy timeline loglari (JSON)
- `GET /run/logs?workspace=ws-...&app=...&format=text` -> okunabilir timeline loglari (text)

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
- ZIP Build ve Local Build UI formlarina dependency secimi eklendi (`none|redis|sql|both`)
- ZIP/Local formlarina SQL zorunlu alanlari eklendi (`database`, `password`)
- ZIP/Local icin advanced dependency override alanlari eklendi (redis/sql image, service, port, env)
- CLI `-apply` akisinda deploy/dependency orchestration `git|zip|local` icin ayni sekilde tetiklenir
