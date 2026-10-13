# Лабораторная 2

## Подготовка окружения

Перед началом работы мне нужно было поставить инструменты для работы с так называемым кубернетес

![img_3.png](img_3.png)

Так как работу веду на макоси, то ставила всё через brew:

```bash
brew install kind helm
```

![img_4.png](img_4.png)

Сначала мистер брю не хотел дать ничего поставить из-за проблем с установленным монгодб, поэтому было необходимо порешать проблемки при помощи траст команд

![img_5.png](img_5.png)

## Этап 0 — сервис в кластере

Далее создала кластер, загрузила в него образ и создала заготовку чарта:

```bash
kind create cluster --name obs
kind load docker-image api:0.1.0 --name obs
helm create lecture-2-observability/charts/api
```

После этих действий нужно чуть пошаманить над чартом — во-первых, поменять тег образа, поменять хелсчек, переставить порт, во-вторых, выкинуть лишние блоки, чтобы не рендерились, не мешались, не мозолили глаза и не нагружали сверх нужного — типовой каркас, например, рассчитан на nginx, который мы не используем

![img.png](img.png)

![img_1.png](img_1.png)

![img_2.png](img_2.png)

Во время очистки файла от лишних блоков я случайно снесла `service.port`, из-за чего порт был пустым. Сама проблема нашлась через запуск с флагом `--dry-run=server`

```bash
helm install api ./charts/api -n app --create-namespace
```

![img_6.png](img_6.png)

```bash
kubectl -n app get pods -w
```

![img_7.png](img_7.png)

```bash
kubectl -n app port-forward svc/api 8080:8080
```

![img_8.png](img_8.png)

Ну и далее проверяем на живость что запустили — хелсчек, ошибки, метрики и логи

![img_9.png](img_9.png)

Тут, кстати, можно заметить, что запрос я отправила один, а на деле их 48 — тут нужно будет при формировании графиков учесть этот моментик

![img_10.png](img_10.png)

![img_11.png](img_11.png)

![img_12.png](img_12.png)

## Метрики: Prometheus и Grafana

Далее поднимаем пром и графану

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
mkdir -p charts/monitoring
helm show values prometheus-community/kube-prometheus-stack > /tmp/kps-defaults.yaml
```

Далее можем заметить, что `serviceMonitorSelector` у нас пустой и по дефолту кубер это читает как «выбрать всё». Но из-за флага `...NilUsesHelmValues: true` этот селектор перехватывается и подставляется `matchLabels: {release: <имя релиза>}`. Так что в ServiceMonitor api подставим `release: <имя релиза>`, чтобы api знал, как у нас назван релиз мониторинга

Также в файле `kube-prometheus-stack.values.yaml` я отключила блоки `kubeEtcd`, `kubeScheduler`, `kubeControllerManager`, `kubeProxy`, так как их таргеты всегда будут DOWN и в будущем алерт менеджер меня завалит алертами

```bash
helm install kps prometheus-community/kube-prometheus-stack -n monitoring --create-namespace -f charts/monitoring/kube-prometheus-stack.values.yaml
```

![img_13.png](img_13.png)

![img_15.png](img_15.png)

![img_16.png](img_16.png)

![img_17.png](img_17.png)

![img_18.png](img_18.png)

![img_19.png](img_19.png)

![img_20.png](img_20.png)

![img_21.png](img_21.png)

![img_22.png](img_22.png)

```bash
helm upgrade api ./charts/api -n app
kubectl -n app get servicemonitor --show-labels
kubectl -n monitoring port-forward svc/kps-kube-prometheus-stack-prometheus 9090
```

![img_23.png](img_23.png)

![img_24.png](img_24.png)

### Grafana и дашборд RED

Далее начинаем работу с графаной. Видим, что в датасурсах есть наш промчик

![img_25.png](img_25.png)

Далее начнём составлять дашборд по принципу RED — Rate, Errors, Duration

![img_26.png](img_26.png)

![img_27.png](img_27.png)

![img_29.png](img_29.png)

![img_28.png](img_28.png)

Если дать нагрузку:

![img_30.png](img_30.png)

![img_31.png](img_31.png)

![img_32.png](img_32.png)

![img_33.png](img_33.png)

```bash
kubectl -n app port-forward svc/api 8080:8080
```

port-forward привязан к поду, а не к Service, и умирает при rollout

![img_34.png](img_34.png)

![img_35.png](img_35.png)

![img_36.png](img_36.png)

![img_37.png](img_37.png)

![img_38.png](img_38.png)

![img_39.png](img_39.png)

![img_40.png](img_40.png)

![img_41.png](img_41.png)

![img_42.png](img_42.png)

## Логи: Loki и Alloy

### Loki

Далее начинаем работать с Loki

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
helm show values grafana/loki > /tmp/loki-defaults.yaml
```

![img_44.png](img_44.png)

```bash
helm template loki grafana/loki -n monitoring -f charts/monitoring/loki.values.yaml | grep "^kind:" | sort | uniq -c
```

![img_45.png](img_45.png)

```bash
helm install loki grafana/loki -n monitoring -f charts/monitoring/loki.values.yaml
kubectl -n monitoring get pods,pvc -l app.kubernetes.io/name=loki
```

![img_46.png](img_46.png)

![img_47.png](img_47.png)

![img_48.png](img_48.png)

### Сборщик логов Alloy

Для грамотной работы Alloy нужно построить пайплайн из компонентов, которые будут передавать друг другу данные

![img_49.png](img_49.png)

![img_50.png](img_50.png)

И тут как бы Alloy поднялся, но в Loki не пришло ни одного лога. Следовательно, словили тихий неприятный отказ. Чтение при этом работает, в Loki пусто, а в логах Alloy ошибок нет.

А проблема-то, как обычно, ХИТРЕЙШАЯ (мммм moniGGGoring):

![img_51.png](img_51.png)

После исправления очепятки обновляем агента

![img_52.png](img_52.png)

![img_53.png](img_53.png)

![img_54.png](img_54.png)

![img_55.png](img_55.png)

![img_56.png](img_56.png)

### Подключение Loki в UI Grafana

![img_58.png](img_58.png)

![img_59.png](img_59.png)

![img_60.png](img_60.png)

![img_61.png](img_61.png)

![img_62.png](img_62.png)

## Трейсы: OpenTelemetry и Jaeger

![img_63.png](img_63.png)

![img_64.png](img_64.png)

### Направление api в Jaeger

Сейчас приложение отправляет только спаны и не хватает env

![img_66.png](img_66.png)

![img_65.png](img_65.png)

![img_67.png](img_67.png)

![img_68.png](img_68.png)

### Связка логов и трейсов

Попробуем руками найти трейс из лога. Берём, например, этот лог и его трейс `aa52afc2bce4e47d4db1bea3df8da5c6`

![img_71.png](img_71.png)

Нашли:

![img_72.png](img_72.png)

Но лучше сделать так, чтобы из графаны можно было по ссылке сразу перейти и глянуть трейс, для этого:

![img_73.png](img_73.png)

Но тут есть ошибка с портом — нужен был 16686

![img_74.png](img_74.png)

Один порт на запись, а другой непосредственно на чтение, и как раз 16686 — это порт на чтение.

Иду в графану проверять и вижу 404

![img_75.png](img_75.png)

как я подумала - надо было просто дописать префикс к урлу

![img_76.png](img_76.png)

И ещё одна ошибка при тесте подключения

![img_77.png](img_77.png)

И тут оказывается, агент вёл меня не туда по плану... Оказывается, Jaeger v2.22 не отдаёт старый HTTP API `/api/services` и `/api/traces`, только `/api/v3/...`.
Источник Jaeger внутри графаны не обязателен, так как графана сама может справиться с derived field.
После обновлений появилось поле TraceID у лога, и можно прямо перейти глянуть трейс

![img_79.png](img_79.png)

![img_78.png](img_78.png)

## Алерты: Prometheus, Alertmanager, Karma

### Правила алертов

![img_80.png](img_80.png)

Добавила 4 алерта для сервиса api
Соответственно добавила алерт на процент ошибок, длительность ответов по апишке и состояние самого сервиса. Все эти алерты необходимы для понимания живости и работы сервиса и это именно то, что может заафектить на пользователя

![img_82.png](img_82.png)

![img_83.png](img_83.png)

![img_84.png](img_84.png)

![img_85.png](img_85.png)

И все рулы отобразились в проме

![img_86.png](img_86.png)

Теперь попробуем воспроизвести один из алертов — самый лёгкий вариант положить сервис, что мы, собственно, и делаем:

```bash
kubectl -n app scale deploy/api --replicas=0
```

Прошло 30 секунд, и видим, что в юайке рул пожелтел

![img_87.png](img_87.png)

Спустя 3 минуты от того, как погасили сервис, рул загорелся

![img_88.png](img_88.png)

И вот в алертменеджере он тоже есть

![img_89.png](img_89.png)

Теперь поднимаем сервис:

```bash
kubectl -n app scale deploy/api --replicas=1
```

В проме позеленело, а алерт резолвнулся

![img_90.png](img_90.png)

![img_91.png](img_91.png)

Загадка Жака Фреско:

![img_57.png](img_57.png)

### Настройка Alertmanager

Создаём новый чарт

![img_93.png](img_93.png)

![img_94.png](img_94.png)

Ошибка, потому что `null` не были обёрнуты в кавычки. После исправления в алертменеджере добавились новые руты

![img_95.png](img_95.png)

Роняем сервис снова, чтобы затестить алерты. Получили:

![img_96.png](img_96.png)

![img_97.png](img_97.png)

И резолв после поднятия:

![img_98.png](img_98.png)

### Настройка Karma

![img_99.png](img_99.png)

![img_100.png](img_100.png)

Теперь провоцируем алерт:

```bash
end=$((SECONDS+420)); while [ $SECONDS -lt $end ]; do curl -sS -o /dev/null localhost:8080/fail; curl -sS -o /dev/null localhost:8080/health; sleep 0.2; done
```

![img_101.png](img_101.png)

Алерт появился в карме

![img_102.png](img_102.png)

---

А это мои петики:

![img_43.png](img_43.png)
