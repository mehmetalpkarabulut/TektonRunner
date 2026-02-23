# TektonRunner End-to-End Akis (Basit ve Net)

Bu dokuman, sistemde **Tekton**, **tekton-runner** ve **Harbor** rollerini bastan sona aciklar.

## 1) Roller

- **Tekton**:
  - CI engine'dir.
  - Source alir (git/zip/local), image build eder, Harbor'a pushlar.
- **tekton-runner**:
  - API ve orchestration katmanidir.
  - JSON istegini alir, Tekton manifestlerini uretir, Tekton'u tetikler.
  - Build bittikten sonra app deploy, dependency (redis/sql), migration, endpoint mapping islemlerini yapar.
- **Harbor**:
  - Registry'dir.
  - Build edilen image burada tutulur.

## 2) Uctan Uca Sequence

1. Kullanici / UI -> `POST /run` ile `tekton-runner`a JSON gonderir.
2. `tekton-runner`:
   - input validation yapar,
   - gerekli ise workspace kind cluster hazirlar (`ws-...`),
   - TaskRun ve ilgili manifestleri olusturur.
3. `tekton-runner` manifestleri apply eder.
4. **Tekton TaskRun** calisir:
   - source fetch (git/zip/local),
   - Dockerfile/build context hazirlama,
   - Kaniko ile image build,
   - Harbor'a push.
5. Tekton basarili bitince kontrol tekrar `tekton-runner`a doner.
6. `tekton-runner` build sonrasi asamalari yapar:
   - dependency (`redis/sql/both`) deploy,
   - opsiyonel migration job,
   - app deployment + service apply,
   - external port forward / endpoint map guncelleme.
7. Kullanici app'e endpoint/external port uzerinden erisir.

## 3) Kritik Ayrim: Harbor Push Sonrasi Kim Ne Yapar?

- **Image'i Harbor'a pushlayan taraf Tekton'dur.**
- **Push sonrasi deployment/orchestration yapan taraf tekton-runner'dir.**

Yani:
- Tekton = image uretim motoru
- tekton-runner = tetikleme + build-sonrasi ortam orkestrasyonu

## 4) Source Tiplerine Gore Durum

`source.type`:
- `git`
- `zip`
- `local`

Bu uc source tipi icin de ayni mantik gecerlidir:
- Tekton build/push yapar,
- tekton-runner deployment ve dependency yonetir.

## 5) Dependency ve Migration

`dependency.type`:
- `none`
- `redis`
- `sql`
- `both`

Notlar:
- `migration.enabled=true` sadece `dependency.type=sql|both` ile gecerlidir.
- SQL/Redis image'larinin Harbor mirror'i varsa workspace daha stabil acilir.

## 6) Runtime Erişim Modeli

- `runner /endpoint` bazen `127.0.0.1:<nodePort>` donebilir (host-local).
- Dis erisimde ana yol: `external-map` portlari (`<HOST_IP>:<external_port>`).
- Ornek:
  - `GET /external-map`
  - `http://<HOST_IP>:18739`

## 7) Sik Karisikliklar

### Soru: "App calismiyor, problem Tekton mi?"

- Tekton gorevi image build/push ile biter.
- App acilmama, dependency, DNS, forward, sql login gibi konular cogunlukla **tekton-runner + workspace runtime** tarafidir.

### Soru: "Harbor'da image var ama app acilmiyor, neden?"

Olasilar:
- workspace node `lenovo` cozemiyor (`no such host`),
- dependency eksik (ornek app Redis bekliyor),
- SQL login hatasi,
- external forward/endpoint portu yanlis.

## 8) Hangi Loga Bakmaliyim?

- Tekton build asamasi:
  - `kubectl get taskrun -n tekton-pipelines`
  - `kubectl logs ... step-build`
- Runner orchestration:
  - `sudo journalctl -u tekton-runner.service -n 200 --no-pager`
- Workspace app/dependency durumu:
  - `kubectl --kubeconfig /home/beko/kubeconfigs/ws-<workspace>.yaml get pods,svc -n ws-<workspace>`

## 9) Ozet

- Tekton: **Build + Push**
- tekton-runner: **Tetikleme + Build Sonrasi Deploy/Dependency/Endpoint**
- Harbor: **Image Deposu**

Bu ayrimi net tutarsan troubleshooting cok hizlanir.
