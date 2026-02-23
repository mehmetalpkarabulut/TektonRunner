# ZIP Server External Access (Persistent)

Bu dokuman, `tekton-pipelines` icindeki `zip-server` servisini host uzerinden ve LAN'daki diger PC'lerden erisilebilir hale getirmek icindir.

## 1) On Kosullar

- `zip-server` Kubernetes service ayakta olmali:

```bash
kubectl --kubeconfig /home/beko/kubeconfigs/tekton.yaml -n tekton-pipelines get svc zip-server
```

- Host IP bu ornekte: `10.134.70.106`
- Hedef URL (LAN): `http://10.134.70.106:18080/app.zip`

## 2) Kalici Systemd Service

`/etc/systemd/system/zip-server-forward.service` dosyasi:

```ini
[Unit]
Description=Expose Tekton zip-server on host port 18080
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/snap/bin/kubectl --kubeconfig /home/beko/kubeconfigs/tekton.yaml -n tekton-pipelines port-forward --address 0.0.0.0 svc/zip-server 18080:8080
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Aktif etme:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now zip-server-forward.service
```

## 3) Dogrulama

Service durumu:

```bash
sudo systemctl status zip-server-forward.service --no-pager
```

Dinlenen port:

```bash
sudo ss -ltnp | rg ':18080'
```

HTTP test:

```bash
curl -I http://127.0.0.1:18080/app.zip
curl -I http://10.134.70.106:18080/app.zip
```

Beklenen: `HTTP/1.0 200 OK` veya `HTTP/1.1 200 OK`.

## 4) TektonRunner Zip URL Ornegi

Runner payload icinde `source.zip_url`:

```text
http://zip-server.tekton-pipelines.svc.cluster.local:8080/app.zip
```

Not:
- Yukaridaki DNS Kubernetes cluster icinden kullanilir.
- Disaridan erisim icin host URL kullanin:
  - `http://10.134.70.106:18080/app.zip`

## 5) Operasyon Komutlari

```bash
sudo systemctl restart zip-server-forward.service
sudo systemctl stop zip-server-forward.service
sudo systemctl start zip-server-forward.service
```
