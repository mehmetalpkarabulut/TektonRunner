# Tekton Runner Operations Checklist

Bu dosya, gunluk kontrol ve ariza durumunda hizli aksiyon icin kisa kontrol listesidir.

## 1) Hizli Saglik Kontrolu

```bash
sudo systemctl is-active tekton-runner.service
sudo systemctl is-active harbor.service
sudo ss -lntp | rg ':8088|:8443|:8080'
curl -sS -i --max-time 5 http://<HOST_IP>:8088/healthz
curl -sS -i --max-time 5 http://<HOST_IP>:8088/ui/
```

Beklenen:
- `tekton-runner.service` = `active`
- `harbor.service` = `active`
- `8088` dinlemede
- `/healthz` = `200 OK`
- `/ui/` = `200 OK`

## 2) UI 404 / API 404 Kontrolu

```bash
curl -sS -i --max-time 5 http://<HOST_IP>:8088/workspaces
curl -sS -i --max-time 5 http://<HOST_IP>:8088/api/workspaces
curl -sS -i --max-time 5 http://<HOST_IP>:8088/api/healthz
```

Beklenen:
- Tum endpointler `200` donmeli.

## 2.1) Quick Actions Kontrolu

UI uzerinde:
- `Git Sample / ZIP Sample / Local Sample` tiklandiginda popup acilmali.
- Popup icinde JSON gorunmeli.
- `Copy JSON` ve `Fill Form` butonlari calismali.
- Git sample JSON icinde `source.git_username` ve `source.git_token` alanlari bulunmali.
- ZIP ve Local formlarinda `Dependency Type` alani gorunmeli (`none|redis|sql|both`).
- ZIP/Local icin `sql` veya `both` seciminde `SQL Database` ve `SQL Password` zorunlu olmali.

## 3) Workspace Pod Durumu

```bash
sudo kind get clusters
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get pods -n ws-<workspace> -o wide
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get svc -n ws-<workspace>
```

Beklenen:
- Podlar `Running`
- Servis olusmus olmali

## 4) ImagePullBackOff Arizasi (lenovo no such host)

Semptom:
- Pod eventlerinde `lookup lenovo ... no such host`
- UI metrics tarafinda `pod_metrics` hatasi

Kontrol:
```bash
sudo docker exec ws-<workspace>-control-plane sh -c "getent hosts lenovo"
sudo docker exec ws-<workspace>-control-plane sh -c "cat /etc/containerd/certs.d/lenovo:8443/hosts.toml"
sudo docker exec ws-<workspace>-control-plane sh -c "grep -n 'config_path' /etc/containerd/config.toml"
```

Beklenen:
- `lenovo` host cozulmeli
- `hosts.toml` icinde `https://lenovo:8443` olmali
- `config_path = "/etc/containerd/certs.d"` olmali

Hizli fix:
```bash
sudo docker exec ws-<workspace>-control-plane sh -c 'gw="$(ip route | sed -n "s/^default via \\([^ ]*\\).*/\\1/p" | head -n1)"; [ -n "$gw" ] || gw="172.18.0.1"; grep -qE "^${gw}[[:space:]]+lenovo$" /etc/hosts || echo "$gw lenovo" >> /etc/hosts; mkdir -p /etc/containerd/certs.d/lenovo:8443'
cat > /tmp/lenovo8443-hosts.toml <<'EOF'
server = "https://lenovo:8443"

[host."https://lenovo:8443"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
EOF
sudo docker cp /tmp/lenovo8443-hosts.toml ws-<workspace>-control-plane:/etc/containerd/certs.d/lenovo:8443/hosts.toml
sudo docker exec ws-<workspace>-control-plane sh -c 'grep -q "config_path = \"/etc/containerd/certs.d\"" /etc/containerd/config.toml || printf "\n[plugins.\"io.containerd.grpc.v1.cri\".registry]\n  config_path = \"/etc/containerd/certs.d\"\n" >> /etc/containerd/config.toml'
sudo docker exec ws-<workspace>-control-plane sh -c 'systemctl restart containerd'
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl delete pod -n ws-<workspace> --all --force --grace-period=0
```

## 5) Metrics Hata Yorumu

Not:
- Yeni pod kalkar kalkmaz metrics gelmeyebilir.
- Ilk 10-30 saniye `metrics not available yet` normaldir.
- Pod `Running` olduktan sonra tekrar kontrol edin.

```bash
curl -sS --max-time 10 "http://127.0.0.1:8088/workspace/metrics?workspace=ws-<workspace>" | jq '.errors, (.pods|length)'
```

## 6) Servis Loglari

```bash
sudo journalctl -u tekton-runner.service -n 120 --no-pager
sudo journalctl -u tekton-runner.service -f
```

## 7) Kod/Servis Guncelleme Akisi

```bash
cd /home/beko/tools/tekton-runner
sudo go build -o tekton-runner ./...
sudo systemctl restart tekton-runner.service
sudo systemctl status tekton-runner.service --no-pager
```

## 8) Dependency Saglik Kontrolu (Redis / SQL)

`dependency.type` kullanilan workspace icin:

```bash
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get deploy,svc,secret -n ws-<workspace>
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get pods -n ws-<workspace> -o wide
```

Beklenen:
- `redis` veya `sql` deployment `Available`
- App pod `Running`
- App icin `*-app-config` secret mevcut

App env override kontrolu:

```bash
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get deploy <app> -n ws-<workspace> -o yaml | rg 'ConnectionStrings__'
```

## 9) Harbor Mirror Kontrolu (Redis / SQL)

```bash
sudo docker images | rg 'redis|mssql-server'
curl -k -sS -u 'admin:Harbor12345' 'https://127.0.0.1:8443/api/v2.0/projects/library/repositories?page_size=200' | rg 'redis|mssql-server'
```

Beklenen:
- `lenovo:8443/library/redis:7-alpine`
- `lenovo:8443/library/mssql-server:2022-latest`

## 10) SQL Migration Kontrolu

Migration job gecmisi:

```bash
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get jobs -n ws-<workspace>
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl logs job/<migrate-job-name> -n ws-<workspace>
```

Beklenen:
- migration job `Complete`
- migration basarisizsa app deployment apply edilmez

## 11) SQL External Port / DBeaver Troubleshooting

Runner external map ve listener kontrolu:

```bash
curl -sS http://127.0.0.1:8088/external-map | rg '"app":"sql"'
ss -ltn | rg '<sql-external-port>'
```

DBeaver ayari:
- Driver: `SQL Server (Microsoft)`
- Auth: `SQL Server Authentication`
- Host: `<HOST_IP>` (ornek: `10.134.70.106`)
- Port: `<sql-external-port>`
- User: `sa`
- SSL: `encrypt=false` veya `trustServerCertificate=true`

Hata: `unexpected pre-login response`

```bash
sudo systemctl restart tekton-runner
```

Neden: SQL forward eski NodePort'a bakiyor olabilir.

Hata: `Login failed for user sa`
- Auth tipi yanlis olabilir.
- Yanlis password veya DBeaver cache.
- Baglantiyi silip yeniden olustur.

Kesin login testi:

```bash
docker run --rm mcr.microsoft.com/mssql-tools /opt/mssql-tools/bin/sqlcmd \
  -S <HOST_IP>:<sql-external-port> -U sa -P '<PASSWORD>' -Q 'SELECT 1'
```

## 12) SQL Memory Limit Kontrolu

```bash
sudo KUBECONFIG=/home/beko/kubeconfigs/ws-<workspace>.yaml kubectl get deploy sql -n ws-<workspace> -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MSSQL_MEMORY_LIMIT_MB")].value}{"\n"}{.spec.template.spec.containers[0].resources}'
```

Beklenen:
- `MSSQL_MEMORY_LIMIT_MB=1024`
- `requests: 250m / 512Mi`
- `limits: 1 CPU / 1536Mi`

## 13) 2026-02-18 Update Notes

Bugun eklenen operasyonel degisiklikler:
- Dependency orchestrations: `none|redis|sql|both`
- SQL migration job kontrolu
- SQL external map/DBeaver hata cozum adimlari
- SQL default memory/resource limitleri
- ZIP/Local Build formlarina da dependency secimi eklendi
