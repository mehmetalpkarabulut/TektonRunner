# Kind + Tekton + Harbor + GitHub Webhook Kurulumu

Bu doküman, kind üzerinde Tekton kurup Harbor registry ile entegre ederek GitHub push event'leriyle otomatik image build/push akışını kurmak için hazırlanmıştır. Test ortamı odaklıdır.

## Önkoşullar

- Linux host
- `docker`, `kubectl`, `kind`, `curl`, `openssl`
- Harbor host adı: `lenovo`
- Harbor admin şifresi: `Harbor12345`

## 1) Kind Cluster Kurulumu

```bash
kind create cluster --name tekton
kind get kubeconfig --name tekton > /tmp/kind-tekton.kubeconfig
export KUBECONFIG=/tmp/kind-tekton.kubeconfig
```

Kontrol:

```bash
kubectl get nodes
```

## 2) Harbor Kurulumu (Docker Compose)

```bash
cd /home/beko/harbor/harbor
sudo docker compose up -d
```

Erişim:
- UI: `https://lenovo:8443`
- Registry: `lenovo:8443`

## 3) Harbor TLS Sertifikası (SAN ile)

Sertifika `lenovo` SAN içermeli.

```bash
sudo openssl req -newkey rsa:2048 -nodes -keyout /home/beko/harbor/certs/harbor.key \
  -x509 -days 365 -out /home/beko/harbor/certs/harbor.crt \
  -subj "/CN=lenovo" \
  -addext "subjectAltName=DNS:lenovo"
```

Harbor nginx sertifikasını güncelle:

```bash
sudo cp /home/beko/harbor/certs/harbor.crt /home/beko/harbor/data/secret/cert/server.crt
sudo cp /home/beko/harbor/certs/harbor.key /home/beko/harbor/data/secret/cert/server.key
sudo chown 10000:10000 /home/beko/harbor/data/secret/cert/server.crt /home/beko/harbor/data/secret/cert/server.key
sudo chmod 644 /home/beko/harbor/data/secret/cert/server.crt
sudo chmod 600 /home/beko/harbor/data/secret/cert/server.key
sudo docker restart nginx
```

## 4) Harbor Projesi Oluştur

```bash
sudo curl -sk -u 'admin:Harbor12345' -H 'Content-Type: application/json' \
  -d '{"project_name":"tektoncd","public":true}' \
  https://lenovo:8443/api/v2.0/projects
```

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

## 6) Tekton Image Pull Sorunu için Harbor'a Push

Host'ta gerekli imageleri çek ve Harbor'a pushla:

```bash
sudo docker pull node:18-alpine
sudo docker pull alpine/git:2.45.2
sudo docker pull gcr.io/kaniko-project/executor:v1.23.2
sudo docker pull gcr.io/kaniko-project/executor:debug
sudo docker pull curlimages/curl:8.12.1

sudo docker tag node:18-alpine lenovo:8443/library/node:18-alpine
sudo docker tag alpine/git:2.45.2 lenovo:8443/library/alpine-git:2.45.2
sudo docker tag gcr.io/kaniko-project/executor:v1.23.2 lenovo:8443/library/kaniko-executor:v1.23.2
sudo docker tag gcr.io/kaniko-project/executor:debug lenovo:8443/library/kaniko-executor:debug
sudo docker tag curlimages/curl:8.12.1 lenovo:8443/library/curl:8.12.1

sudo docker push lenovo:8443/library/node:18-alpine
sudo docker push lenovo:8443/library/alpine-git:2.45.2
sudo docker push lenovo:8443/library/kaniko-executor:v1.23.2
sudo docker push lenovo:8443/library/kaniko-executor:debug
sudo docker push lenovo:8443/library/curl:8.12.1
```

## 7) Harbor Sertifikasını Tekton'a Tanıt

```bash
kubectl -n tekton-pipelines apply -f /tmp/registry-cert.yaml
kubectl -n tekton-pipelines rollout restart deployment tekton-pipelines-controller tekton-pipelines-webhook
```

## 8) Tekton Trigger + Task Manifesti

Manifest: `/tmp/tekton-trigger.yaml`

Bu manifest şunları içerir:
- GitHub webhook secret
- Harbor dockerconfig secret
- Task: `build-and-push`
- Trigger: GitHub `push` event
- Image formatı: `lenovo:8443/{repo}/{repo}:{commitsha}`

Uygula:

```bash
kubectl apply -f /tmp/tekton-trigger.yaml
```

## 9) Pod Security Etiketi

```bash
kubectl label namespace tekton-pipelines pod-security.kubernetes.io/enforce=baseline --overwrite
```

## 10) Webhook için Cloudflared Tünel

Port-forward:

```bash
sudo KUBECONFIG=/tmp/kind-tekton.kubeconfig kubectl -n tekton-pipelines port-forward svc/github-listener 18080:8080
```

Cloudflared:

```bash
sudo nohup cloudflared tunnel --url http://127.0.0.1:18080 --no-autoupdate --protocol http2 > /tmp/cloudflared.log 2>&1 &
```

Tunnel URL (logdan):

```bash
rg -o 'https://[A-Za-z0-9.-]+\\.trycloudflare\\.com' /tmp/cloudflared.log | tail -n 1
```

## 11) GitHub Webhook Ayarları

Repo > Settings > Webhooks > Add webhook:

- URL: `https://<trycloudflare-url>`
- Content type: `application/json`
- Secret: `tekton-webhook-secret`
- Event: `push`

## 12) Doğrulama

TaskRun oluşuyor mu:

```bash
kubectl get taskrun -n tekton-pipelines --sort-by=.metadata.creationTimestamp | tail -n 5
```

Harbor proje kontrol:

```bash
sudo curl -sk -u 'admin:Harbor12345' https://lenovo:8443/api/v2.0/projects?name=<repo>
```

## 13) Beklenen Çıktı

Push sonrası Harbor'da şu formatta image oluşur:

```
lenovo:8443/<repo>/<repo>:<commitsha>
```

## 14) Sık Problemler

- Webhook 502: Port-forward veya cloudflared düşmüş olabilir.
- Trigger çalışmıyor: EventListener loglarını kontrol et.
- Kaniko step hata: `kaniko-executor:debug` kullanılır.
- Dockerfile pull: `--skip-tls-verify` açık.

