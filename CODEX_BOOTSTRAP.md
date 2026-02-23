# CODEX BOOTSTRAP RUNBOOK (TektonRunner)

Bu dokuman, yeni bir makinede bu repo ile Harbor + Tekton + tekton-runner ortamini ayaga kaldirmak, dogrulamak ve sorun gidermek icin yazilmistir.

## 1) Hedef Mimari

- Host: Linux
- Harbor (Docker Compose):
  - UI: `https://lenovo:8443`
  - Registry: `lenovo:8443`
- Tekton cluster: kind (`tekton`)
- Tekton Task: `build-and-push-generic`
- Runner API: `tekton-runner` (`:8088`)
- Runner UI: `/ui/`

## 2) Repo Icerigi

- Runner kodu: `tools/tekton-runner`
- Tekton task/manifests: `manifests/`
- Operasyon dokumanlari: `docs/`

## 3) Onkosullar

- `docker`, `kubectl`, `kind`, `curl`, `openssl`, `go`
- Host'ta `lenovo` DNS/hosts cozumlemesi
- Harbor sertifikasi: `/home/beko/harbor/certs/harbor.crt` ve `/home/beko/harbor/certs/harbor.key`

## 4) Harbor Kurulumu

```bash
cd /home/beko/harbor/harbor
sudo docker compose up -d
```

Kontrol:

```bash
sudo docker compose ps
curl -k -I https://lenovo:8443
```

Varsayilan login:

- user: `admin`
- pass: `Harbor12345`

## 5) Kind + Tekton Kurulumu

```bash
kind create cluster --name tekton
kind get kubeconfig --name tekton > /home/beko/kubeconfigs/tekton.yaml
export KUBECONFIG=/home/beko/kubeconfigs/tekton.yaml

kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml
kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/release.yaml
kubectl apply -f https://storage.googleapis.com/tekton-releases/triggers/latest/interceptors.yaml
```

Kontrol:

```bash
kubectl get pods -n tekton-pipelines
```

## 6) Harbor Mirror Image'lari

Ornek mirror seti:

```bash
sudo docker pull node:18-alpine
sudo docker pull alpine/git:2.45.2
sudo docker pull gcr.io/kaniko-project/executor:debug
sudo docker pull curlimages/curl:8.12.1
sudo docker pull python:3.12-alpine
sudo docker pull redis:7-alpine
sudo docker pull mcr.microsoft.com/mssql/server:2022-latest

sudo docker tag node:18-alpine lenovo:8443/library/node:18-alpine
sudo docker tag alpine/git:2.45.2 lenovo:8443/library/alpine-git:2.45.2
sudo docker tag gcr.io/kaniko-project/executor:debug lenovo:8443/library/kaniko-executor:debug
sudo docker tag curlimages/curl:8.12.1 lenovo:8443/library/curl:8.12.1
sudo docker tag python:3.12-alpine lenovo:8443/library/python:3.12-alpine
sudo docker tag redis:7-alpine lenovo:8443/library/redis:7-alpine
sudo docker tag mcr.microsoft.com/mssql/server:2022-latest lenovo:8443/library/mssql-server:2022-latest

sudo docker push lenovo:8443/library/node:18-alpine
sudo docker push lenovo:8443/library/alpine-git:2.45.2
sudo docker push lenovo:8443/library/kaniko-executor:debug
sudo docker push lenovo:8443/library/curl:8.12.1
sudo docker push lenovo:8443/library/python:3.12-alpine
sudo docker push lenovo:8443/library/redis:7-alpine
sudo docker push lenovo:8443/library/mssql-server:2022-latest
```

## 7) Tekton Manifest Uygulama

```bash
kubectl apply -f manifests/tekton-generic-build.yaml
kubectl apply -f manifests/tekton-runner-rbac.yaml
```

## 8) Runner Build + Service

```bash
cd tools/tekton-runner
go build -o tekton-runner ./...
```

Systemd ornegi (hostta mevcut ise):

```bash
sudo systemctl restart tekton-runner.service
sudo systemctl status tekton-runner.service --no-pager
```

Kontrol:

```bash
curl -i http://127.0.0.1:8088/healthz
curl -i http://127.0.0.1:8088/ui/
```

## 9) API Ozeti

- `POST /run`
- `POST /run?dry_run=true`
- `GET /workspaces`
- `GET /workspace/status?workspace=ws-...`
- `GET /endpoint?workspace=ws-...&app=...`
- `GET /external-map`

## 10) JSON Ozeti

`source.type`:

- `git`
- `zip`
- `local`

`dependency.type`:

- `none`
- `redis`
- `sql`
- `both`

`migration.enabled=true` sadece `dependency.type=sql|both` ile kullanilir.

## 11) UI Durumu

UI'da `Git Build`, `ZIP Build`, `Local Build` bolumleri ayni capability setine sahiptir:

- dependency secimi (`none|redis|sql|both`)
- SQL zorunlu alanlari (`database`, `password`)
- migration alanlari
- advanced override alanlari (image/service/port/env)

## 12) En Sik Sorunlar

### A) ImagePullBackOff + `lookup lenovo ... no such host`

Workspace kind node icinde fix:

```bash
NODE=ws-<workspace>-control-plane
sudo docker exec "$NODE" sh -lc 'gw="$(ip route | sed -n "s/^default via \([^ ]*\).*/\1/p" | head -n1)"; [ -n "$gw" ] || gw="172.18.0.1"; grep -qE "^${gw}[[:space:]]+lenovo$" /etc/hosts || echo "$gw lenovo" >> /etc/hosts; mkdir -p /etc/containerd/certs.d/lenovo:8443'
cat >/tmp/lenovo8443-hosts.toml <<'EOT'
server = "https://lenovo:8443"

[host."https://lenovo:8443"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
EOT
sudo docker cp /tmp/lenovo8443-hosts.toml "$NODE":/etc/containerd/certs.d/lenovo:8443/hosts.toml
sudo docker exec "$NODE" sh -lc 'grep -q "config_path = \"/etc/containerd/certs.d\"" /etc/containerd/config.toml || printf "\n[plugins.\"io.containerd.grpc.v1.cri\".registry]\n  config_path = \"/etc/containerd/certs.d\"\n" >> /etc/containerd/config.toml; systemctl restart containerd'
```

### B) App ulasilamiyor

- Runner endpoint'i host-local olabilir (`127.0.0.1:<port>`). Dis erisim icin `external-map` portunu kullan.
- Ornek: `http://<HOST_IP>:18739`

### C) `/deps` SQL login hatasi

- Secret icindeki SQL password ile SQL pod auth secret'i uyumsuz olabilir.
- `kubectl get secret ... -o yaml` ile karsilastir.

### D) App Redis'e bagimli ama Redis yok

- Uygulama baslasa bile HTTP cevap vermeyebilir.
- `dependency.type=redis|both` ile deploy et veya workspace'e Redis service/deployment ekle.

## 13) Operasyon Komutlari

```bash
sudo systemctl status tekton-runner.service --no-pager
sudo journalctl -u tekton-runner.service -n 200 --no-pager
curl -sS http://127.0.0.1:8088/workspaces
curl -sS "http://127.0.0.1:8088/workspace/status?workspace=ws-<workspace>"
curl -sS "http://127.0.0.1:8088/external-map"
```

## 14) Git Akisi (Zorunlu)

Her degisiklikten sonra:

```bash
git add -A
git commit -m "<kisa-aciklama>"
git push origin main
```

Rollback icin:

```bash
git log --oneline --decorate -n 20
git revert <commit>
git push origin main
```

## 15) Guvenlik Notu

Bu runbook test ortam varsayimlari icerir (or. `skip_verify=true`, sabit sifreler). Uretimde:

- registry CA trust modelini duzgun yonetin,
- sifreleri secret manager ile yonetin,
- public/protected projeleri netlestirin,
- audit/log saklama politikasini uygulayin.
