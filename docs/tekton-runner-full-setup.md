# Tekton Runner: Kind + Tekton + Harbor + HTTP API (Git/Local/ZIP)

## Proje Özeti

Bu çalışma ile bir Kubernetes (kind) ortamında Tekton kuruldu ve Harbor registry ile entegre edildi. Bir HTTP servis (`tekton-runner`) sayesinde uygulama dışarıdan JSON göndererek build tetikleyebiliyor. Kaynak olarak GitHub/Azure DevOps (HTTPS token ile), yerel NFS/SMB mount ve ZIP indirip açma destekleniyor. Tekton Task, kaynak kodu alıp Kaniko ile image build ediyor ve Harbor’a pushluyor. Böylece webhook kullanmadan, dış sistemlerin JSON ile tetikleyebileceği esnek bir CI pipeline sağlanmış oluyor.

---

## 1) Önkoşullar

- Linux host
- `docker`, `kubectl`, `kind`, `curl`, `openssl`
- Harbor erişimi: `https://lenovo:8443`
- Harbor admin şifre: `Harbor12345`
- İnternet erişimi (ilk kurulum için gerekli)

---

## 2) Kind Kurulumu

```bash
kind create cluster --name tekton
kind get kubeconfig --name tekton > /tmp/kind-tekton.kubeconfig
export KUBECONFIG=/tmp/kind-tekton.kubeconfig
```

Kontrol:
```bash
kubectl get nodes
```

---

## 3) Harbor Kurulumu

```bash
cd /home/beko/harbor/harbor
sudo docker compose up -d
```

---

## 4) Harbor Sertifika (SAN)

```bash
sudo openssl req -newkey rsa:2048 -nodes -keyout /home/beko/harbor/certs/harbor.key \
  -x509 -days 365 -out /home/beko/harbor/certs/harbor.crt \
  -subj "/CN=lenovo" \
  -addext "subjectAltName=DNS:lenovo"

sudo cp /home/beko/harbor/certs/harbor.crt /home/beko/harbor/data/secret/cert/server.crt
sudo cp /home/beko/harbor/certs/harbor.key /home/beko/harbor/data/secret/cert/server.key
sudo chown 10000:10000 /home/beko/harbor/data/secret/cert/server.crt /home/beko/harbor/data/secret/cert/server.key
sudo chmod 644 /home/beko/harbor/data/secret/cert/server.crt
sudo chmod 600 /home/beko/harbor/data/secret/cert/server.key
sudo docker restart nginx
```

---

## 5) Tekton Kurulumu

```bash
kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml
kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/release.yaml
kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/interceptors.yaml
```

Kontrol:
```bash
kubectl get pods -n tekton-pipelines
```

---

## 6) Harbor’a Gerekli Image’leri Push (Offline çekim için)

```bash
sudo docker pull node:18-alpine
sudo docker pull alpine/git:2.45.2
sudo docker pull gcr.io/kaniko-project/executor:debug
sudo docker pull curlimages/curl:8.12.1
sudo docker pull python:3.12-alpine

sudo docker tag node:18-alpine lenovo:8443/library/node:18-alpine
sudo docker tag alpine/git:2.45.2 lenovo:8443/library/alpine-git:2.45.2
sudo docker tag gcr.io/kaniko-project/executor:debug lenovo:8443/library/kaniko-executor:debug
sudo docker tag curlimages/curl:8.12.1 lenovo:8443/library/curl:8.12.1
sudo docker tag python:3.12-alpine lenovo:8443/library/python:3.12-alpine

sudo docker push lenovo:8443/library/node:18-alpine
sudo docker push lenovo:8443/library/alpine-git:2.45.2
sudo docker push lenovo:8443/library/kaniko-executor:debug
sudo docker push lenovo:8443/library/curl:8.12.1
sudo docker push lenovo:8443/library/python:3.12-alpine
```

---

## 7) Tekton Build Task (Git/Local/ZIP)

Manifest:
```
/home/beko/manifests/tekton-generic-build.yaml
```

Uygula:
```bash
kubectl apply -f /home/beko/manifests/tekton-generic-build.yaml
```

Bu Task:
- Git repo clone
- Local NFS/SMB mount
- ZIP indirip açma
- Kaniko ile build + Harbor push

Dockerfile otomatik algılama:
- ZIP içinde tek bir Dockerfile varsa otomatik bulunur
- Birden fazla Dockerfile varsa hata verir

---

## 8) Tekton Runner (HTTP API)

Kod:
```
/home/beko/tools/tekton-runner
```

Build:
```bash
cd /home/beko/tools/tekton-runner
go build -o tekton-runner ./...
```

Çalıştırma:
```bash
./tekton-runner -server -addr 0.0.0.0:8088
```

Kalici calistirma (onerilen):
```bash
sudo systemctl enable --now tekton-runner.service
sudo systemctl status tekton-runner.service --no-pager
```

Health check:
```
http://<HOST_IP>:8088/healthz
```

Run endpoint:
```
http://<HOST_IP>:8088/run
```

Not (2026-02-16):
- UI geriye donuk uyumluluk icin `/api/*` endpoint aliaslari desteklenir.
- Yeni workspace node olusumunda `lenovo` host mapping + containerd `hosts.toml` otomatik yazilir.
- Yeni pod olusumundan hemen sonra kisa sureli `metrics not available yet` gorulebilir.
- Quick Actions:
  - `Git Sample / ZIP Sample / Local Sample` popup acarak JSON gosterir.
  - Popup icinden `Copy JSON` ve `Fill Form` kullanilabilir.
  - Git sample icinde `git_username` ve `git_token` placeholder alanlari bulunur.

Not (2026-02-23):
- ZIP Build ve Local Build formlarinda da dependency secimi vardir (`none|redis|sql|both`).
- ZIP/Local formlarinda SQL dependency secilirse `SQL Database` ve `SQL Password` zorunludur.
- `POST /run` icin dependency orchestration (`redis/sql/both`) `git|zip|local` kaynak tiplerinin tumunda calisir.

---

## 9) RBAC (Opsiyonel)

```bash
kubectl apply -f /home/beko/manifests/tekton-runner-rbac.yaml
```

Bu RBAC ile:
- TaskRun
- Secret
- PVC
- PV oluşturma izinleri verilir

---

## 10) JSON Örnekleri

### GitHub/Azure (HTTPS Token)

```json
{
  "source": {
    "type": "git",
    "repo_url": "https://github.com/mehmetalpkarabulut/Dev",
    "revision": "main",
    "git_username": "GITHUB_USER",
    "git_token": "GITHUB_TOKEN"
  },
  "image": {
    "project": "dev",
    "tag": "latest",
    "registry": "lenovo:8443"
  }
}
```

### Local NFS

```json
{
  "source": {
    "type": "local",
    "local_path": "projeler/myapp",
    "nfs": {
      "server": "10.0.0.10",
      "path": "/exports/projects",
      "size": "50Gi"
    }
  },
  "image": {
    "project": "myapp",
    "tag": "latest",
    "registry": "lenovo:8443"
  }
}
```

### Local SMB

```json
{
  "source": {
    "type": "local",
    "local_path": "projeler/myapp",
    "smb": {
      "server": "fileserver.local",
      "share": "projects",
      "username": "SMB_USER",
      "password": "SMB_PASS",
      "size": "50Gi"
    }
  },
  "image": {
    "project": "myapp",
    "tag": "latest",
    "registry": "lenovo:8443"
  }
}
```

### ZIP (HTTP üzerinden indir)

```json
{
  "source": {
    "type": "zip",
    "zip_url": "https://example.com/app.zip",
    "zip_username": "ZIP_USER",
    "zip_password": "ZIP_PASS"
  },
  "image": {
    "project": "myapp",
    "tag": "latest",
    "registry": "lenovo:8443"
  }
}
```

---

## 11) Postman Ayarları

- Method: `POST`
- URL: `http://<HOST_IP>:8088/run`
- Header: `Content-Type: application/json`
- Body: JSON (yukarıdaki örneklerden biri)

---

## 12) Log ve İzleme

TaskRun listeleme:
```bash
kubectl get taskrun -n tekton-pipelines --sort-by=.metadata.creationTimestamp | tail -n 5
```

TaskRun detay:
```bash
kubectl describe taskrun -n tekton-pipelines <TASKRUN>
```

Pod logları:
```bash
kubectl logs -n tekton-pipelines pod/<POD_ADI> -c step-build
```

---

## 13) Sık Sorunlar

- `kubectl create failed`: JSON/parametre hatası olabilir
- `TaskRunResolutionFailed`: Task kurulu değilse olur
- `Dockerfile not found`: ZIP içinde Dockerfile yoksa çıkar
- `network bridge not found`: host üzerinde docker network sorunu

---

## 14) Çalışan Servis

- HTTP Server: `tekton-runner` (port 8088)
- Tekton Task: `build-and-push-generic`
- Harbor: `lenovo:8443`

---

## 15) Workspace Dependency (Redis / SQL) + Secret Override

`tekton-runner`, `dependency` alani ile workspace icinde Redis veya SQL podu ayaga kaldirabilir.

Calisma sirasi:
1. Workspace cluster olusur.
2. Dependency deployment/service apply edilir.
3. `rollout status` ile dependency hazirligi beklenir.
4. Connection string workspace namespace'inde Secret'a yazilir.
5. App deployment bu Secret'i env olarak alir (`ConnectionStrings__...`).

Ornek `dependency`:

```json
"dependency": {
  "type": "redis",
  "redis": {
    "image": "lenovo:8443/library/redis:7-alpine",
    "service_name": "redis",
    "port": 6379,
    "connection_env": "ConnectionStrings__Redis"
  }
}
```

SQL ornegi:

```json
"dependency": {
  "type": "sql",
  "sql": {
    "image": "lenovo:8443/library/mssql-server:2022-latest",
    "service_name": "sql",
    "port": 1433,
    "database": "AppDb",
    "username": "sa",
    "password": "StrongPass_123!",
    "connection_env": "ConnectionStrings__DefaultConnection"
  }
}
```

Not:
- `appsettings.json` dosyasi degistirilmez.
- Runtime'da env override kullanilir.
- Parola ve connection string Kubernetes Secret'ta tutulur.

## 16) Harbor Mirror: Redis / SQL image

```bash
sudo docker pull redis:7-alpine
sudo docker tag redis:7-alpine lenovo:8443/library/redis:7-alpine
sudo docker push lenovo:8443/library/redis:7-alpine

sudo docker pull mcr.microsoft.com/mssql/server:2022-latest
sudo docker tag mcr.microsoft.com/mssql/server:2022-latest lenovo:8443/library/mssql-server:2022-latest
sudo docker push lenovo:8443/library/mssql-server:2022-latest
```

Bu sayede workspace node'lari dependency image'lerini internet yerine Harbor'dan ceker.

## 17) SQL Migration Job (Uygulama Once)

Istekte `migration.enabled=true` verildiginde `tekton-runner` sirayi su sekilde uygular:
1. SQL dependency ayaga kalkar.
2. Migration Job calisir ve `Complete` olmasi beklenir.
3. Basariliysa app deployment apply edilir.

Ornek:

```json
"migration": {
  "enabled": true,
  "image": "lenovo:8443/library/dotnet-sdk:8.0",
  "command": ["dotnet", "ef", "database", "update"],
  "env_name": "ConnectionStrings__DefaultConnection"
}
```

Migration fail olursa app deploy adimi calismaz; hata migration loglariyla doner.

## 18) SQL External Port ve DBeaver Baglanti Notlari

SQL servisi HTTP degildir. Tarayici ile test edilmez; SQL client (DBeaver/sqlcmd) gerekir.

### SQL external map

Runner external map kaydi varsa SQL dis erisim su formatta olur:

- Host: `<HOST_IP>` (ornek: `10.134.70.106`)
- Port: `<sql-external-port>` (ornek: `18772`)

Kontrol:

```bash
curl -sS http://127.0.0.1:8088/external-map
ss -ltn | rg '<sql-external-port>'
```

### DBeaver dogru ayar

- Driver: `SQL Server (Microsoft)`
- Authentication: `SQL Server Authentication` / `Database Native`
- Host: `<HOST_IP>`
- Port: `<sql-external-port>`
- User: `sa`
- Password: istekte verilen SQL password
- SSL: `encrypt=false` veya `trustServerCertificate=true`
- Ilk testte `Database` alani bos birakilabilir.

JDBC ornek:

```text
jdbc:sqlserver://<HOST_IP>:<sql-external-port>;encrypt=false;trustServerCertificate=true
```

### Sik hatalar

- `unexpected pre-login response`:
  - Genelde SQL yerine yanlis port/protokole baglanti.
  - Runner forward eski NodePort'a bakiyor olabilir. `tekton-runner` restart edilip forward yenilenmelidir.
- `Login failed for user sa`:
  - DBeaver auth tipi yanlis (`Windows/AD` secili olabilir).
  - Yanlis password veya cachelenmis eski sifre.
  - DBeaver baglantisini silip yeniden olusturup tekrar deneyin.

sqlcmd ile dogrulama:

```bash
docker run --rm mcr.microsoft.com/mssql-tools /opt/mssql-tools/bin/sqlcmd \
  -S <HOST_IP>:<sql-external-port> -U sa -P '<PASSWORD>' -Q 'SELECT SUSER_SNAME()'
```

## 19) SQL Memory/Resource Varsayilanlari

`dependency.type=sql` veya `both` icin olusan SQL deployment'ta varsayilan guvenlik limitleri:

- `MSSQL_MEMORY_LIMIT_MB=1024`
- Pod requests: `cpu=250m`, `memory=512Mi`
- Pod limits: `cpu=1`, `memory=1536Mi`

Bu limitler dusuk kaynakli hostlarda SQL'in asiri bellek tuketimini sinirlamak icin eklendi.

## 20) 2026-02-18 Degisiklik Ozeti

- Git Build akisina dependency secimi eklendi (`none|redis|sql|both`).
- SQL dependency icin migration job sirasi eklendi (app deploy oncesi).
- Harbor mirror seti genisletildi: `redis`, `mssql-server`, `dotnet-sdk`.
- SQL external port / DBeaver troubleshooting dokumante edildi.
- SQL podlari icin varsayilan kaynak limitleri eklendi.

## 21) 2026-02-23 Degisiklik Ozeti

- ZIP Build ve Local Build UI formlarina dependency secimi eklendi (`none|redis|sql|both`).
- ZIP/Local formlarinda SQL icin zorunlu alan kontrolu eklendi (`database`, `password`).
- ZIP/Local sample JSON'larina dependency ornekleri eklendi.
- CLI `-apply` akisinda deploy/dependency orchestration `git|zip|local` kaynak tiplerinin hepsi icin calisir.
