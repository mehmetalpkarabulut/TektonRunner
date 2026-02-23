# kind-tekton Operasyon Komutlari

Bu dokuman `kind-tekton` cluster'i icinde Tekton ve sistem podlarini nasil goreceginizi ve ne ise yaradiklarini ozetler.

## Kubeconfig ve Context

```bash
KCFG=/home/beko/kubeconfigs/tekton.yaml
kubectl --kubeconfig "$KCFG" config current-context
kubectl --kubeconfig "$KCFG" get nodes -o wide
```

Beklenen context: `kind-tekton`

## Podlari Listeleme

Tum namespace:

```bash
kubectl --kubeconfig "$KCFG" get pods -A -o wide
```

Sadece Tekton namespace:

```bash
kubectl --kubeconfig "$KCFG" get pods -n tekton-pipelines -o wide
```

Sadece kube-system:

```bash
kubectl --kubeconfig "$KCFG" get pods -n kube-system -o wide
```

## Hangi Pod Ne Ise Yarar?

`tekton-pipelines`:
- `tekton-pipelines-controller-*`: TaskRun/PipelineRun orkestrasyonu.
- `tekton-pipelines-webhook-*`: Tekton CRD admission/validation.
- `tekton-events-controller-*`: Tekton event yonetimi.
- `tekton-triggers-controller-*`: Trigger kaynaklarini yonetir.
- `tekton-triggers-webhook-*`: Triggers admission/validation.
- `tekton-triggers-core-interceptors-*`: Trigger interceptor servisi.
- `el-github-listener-*`: EventListener podu (GitHub webhook/event girisi).
- `build-and-push-run-*-pod`: Build calisan podlari (gecmis TaskRun podlari).
- `zip-server`: ZIP dosyasi servis podu (varsa).

`tekton-pipelines-resolvers`:
- `tekton-pipelines-remote-resolvers-*`: Uzak kaynak resolver.

`kube-system`:
- `kube-apiserver-*`, `kube-controller-manager-*`, `kube-scheduler-*`, `etcd-*`: Control-plane.
- `coredns-*`: Cluster DNS.
- `kube-proxy-*`, `kindnet-*`: Service routing ve CNI.
- `metrics-server-*`: Kaynak metrikleri.

`local-path-storage`:
- `local-path-provisioner-*`: Local PV/PVC provisioning.

## Pod Detay, Log, Event

Pod detay:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines describe pod <POD_NAME>
```

Pod log:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines logs <POD_NAME>
```

TaskRun podu step log:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines logs <POD_NAME> -c step-build
```

Son eventler:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines get events --sort-by=.lastTimestamp | tail -n 50
```

## TaskRun Odakli Komutlar

TaskRun listesi:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines get taskrun --sort-by=.metadata.creationTimestamp
```

TaskRun detay:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines describe taskrun <TASKRUN_NAME>
```

TaskRun'a ait pod ismi:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines get taskrun <TASKRUN_NAME> -o jsonpath='{.status.podName}{"\n"}'
```

## Zip Server Kontrolu

Servis/pod:

```bash
kubectl --kubeconfig "$KCFG" -n tekton-pipelines get svc zip-server
kubectl --kubeconfig "$KCFG" -n tekton-pipelines get pod zip-server -o wide
kubectl --kubeconfig "$KCFG" -n tekton-pipelines get endpoints zip-server -o yaml
```

Not:
- `zip-server.tekton-pipelines.svc.cluster.local` DNS'i cluster icinden erisilir.
- Host makineden dogrudan bu DNS cozulmez.
