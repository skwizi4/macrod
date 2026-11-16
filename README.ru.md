# macro-collector

[English](README.md) · **Русский**

Go-демон: собирает макроданные (FRED, Treasury, ЕЦБ, Бундесбанк, Eurostat, Yahoo, тексты ФРС/ЕЦБ, Telegram, SEC EDGAR и др.), хранит их в SQLite, считает метрики и раз в неделю экспортирует `.md` для LLM-агентов (Claude Projects); дашборд с графиками — `macroctl serve` / `macroctl site`.

Смысл показателей — [`DATA_MAP.md`](DATA_MAP.md), источники, ключи и условия использования — [`docs/data-sources.md`](docs/data-sources.md). Лицензия — MIT ([`LICENSE`](LICENSE)); данные не распространяются, каждый подключает свои ключи.

## Установка

Нужны Go 1.26+ и `staticcheck` (для `make lint`):

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
```

cgo не нужен (SQLite — `modernc.org/sqlite`), поэтому сборка под сервер — обычная кросс-компиляция (`make build-linux`).

## Настройка

1. Конфиг: `cp config.example.yaml config.yaml` и поправьте под себя (как минимум `edgar.user_agent` — ваш e-mail, его требует SEC). Личный `config.yaml` в git не попадает. Секретов в нём нет.
2. Секреты — только в env. Скопируйте `.env.example` в `.env` и заполните `FRED_API_KEY` (fred.stlouisfed.org → My Account → API Keys) (обязателен). `GEMINI_API_KEY` (Google AI Studio → API keys; бесплатный уровень, карта не нужна) нужен, только если включены `telegram` и `classify.gemini` — в примере и пресетах они выключены. `.env` в `.gitignore`. Переменные, уже заданные в окружении, имеют приоритет над `.env`. Ключи не присылайте в чат и не показывайте на скриншотах; если ключ засветился — выпустите новый.
3. `data_dir` — где лежат БД, логи, `exchange/` (экспорт для Claude Projects) и `reports/` (сохранённые ответы агентов). По умолчанию `./data` рядом с конфигом (в `.gitignore`). Приоритет: флаг `--data-dir` > env `MACRO_DATA_DIR` > `data_dir` в конфиге.
4. Проверка: `macroctl doctor` — конфиг, ключи, по одному запросу к каждому источнику, запись в БД, место на диске. Код возврата ≠ 0, если что-то не так.

Маркеры `[[FILL]]` и `[[LOOKUP]]` в конфиге относятся к будущим фазам; загрузчик их пока не читает.

## Запуск

```bash
make build      # bin/macrod и bin/macroctl
make run        # демон локально, Ctrl+C — корректная остановка
make check      # go vet + staticcheck + go test -race
```

### Docker

```bash
cp config.example.yaml config.yaml   # и поправить
cp .env.example .env                 # и вписать ключи
mkdir -p data                        # на Linux ещё: sudo chown 10001:10001 data
docker compose up -d                 # сборка образа и запуск macrod
docker compose exec macrod macroctl --config /app/config.yaml doctor
docker compose logs -f               # консольный лог демона
```

> ⚠ **Пока демон работает в Docker на Mac, не открывайте `data/macro.db` с хоста.** Это касается `sqlite3`, `./bin/macroctl` и целей `make status|export-dry|calendar|backfill|run`. Блокировки SQLite и общая память WAL не работают между macOS и VM Docker Desktop: процесс на хосте может обрезать WAL, и в контейнере начинаются `disk I/O error (522)` (было 2026-11-06). Команды — через контейнер: `docker compose exec macrod macroctl --config /app/config.yaml <команда>`. Либо сначала `make down`. На Linux-сервере том — обычная папка, но правило то же: один процесс-владелец, остальное через `exec`.

Образ: multi-stage, итог — alpine, пользователь не root (uid 10001). Папка `./data` — том, `config.yaml` монтируется только для чтения, ключи — из `.env` (`env_file`). `restart: unless-stopped`, healthcheck — `macroctl doctor --local` (без сети). `docker compose down` даёт демону до 60 с на корректную остановку. Те же команды — `make up`, `make down`, `make logs`, `make doctor`.

### Доставка экспорта и уведомления (фазы 6, 14)

После каждого экспорта (недельного, FOMC и ручного `macroctl export`) демон коммитит `exchange/` и пушит его в приватный репозиторий `git.repo`. Затем шлёт сообщение в Telegram. Ошибка push экспорт не ломает: 3 повтора за ~20 мин, потом раз в час (задача «git push pending»). `macroctl export --no-deliver` только пишет файлы.

1. **Репозиторий данных.** Создайте пустой приватный репо (например `you/macro_exchange`) и укажите его в `config.yaml` → `git.repo`, `git.enabled: true`.
2. **Deploy key с правом записи** (у GitHub один ключ — один репо):
   ```bash
   mkdir -p secrets && ssh-keygen -t ed25519 -N "" -C macro-exchange -f secrets/deploy_key
   chmod 600 secrets/deploy_key        # на Linux ещё: sudo chown 10001:10001 secrets/deploy_key
   ```
   Публичную часть `secrets/deploy_key.pub` добавьте в репо: Settings → Deploy keys → Add deploy key, отметьте **Allow write access**. Папка `secrets/` в git не попадает. В контейнер она монтируется только для чтения, путь — `GIT_SSH_KEY_PATH=/app/secrets/deploy_key`. Ключ хоста GitHub проверяется по `deploy/github_known_hosts` (ED25519 `SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU`, docs.github.com).
3. **Telegram-бот.** Создайте бота у @BotFather и запишите токен в `.env` → `TG_BOT_TOKEN`. Напишите боту `/start`, затем откройте `https://api.telegram.org/bot<токен>/getUpdates`: `message.chat.id` — это ваш `TG_CHAT_ID`. В конфиге — `notify.telegram_bot: true`.
   Сообщения:
   - «Недельный экспорт 2026-W45 готов: N файлов» + коммит;
   - «FOMC statement экспортирован»;
   - «⚠ Источник недоступен > 24 ч» (проверка ежечасно, по семейству источников не чаще раза в сутки).
4. Проверка: `make doctor`. Строки `git remote` и `telegram bot` должны быть ✅.
5. **Webhook (фаза 14, необязательно).** Тот же экспорт можно отправить в свою систему (n8n, Zapier, свой сервер): `webhook.enabled: true`, в `.env` — `WEBHOOK_URL` (адрес приёмника; может содержать токен, поэтому не в конфиге) и, по желанию, `WEBHOOK_SECRET`. Демон шлёт `POST` с JSON:
   ```json
   {"event":"export","kind":"weekly","region":"usa","region_title":"США","iso_week":"2026-W46",
    "files":["usa/master/markets_usa.md","usa/news/current_week.md"],
    "git":{"commit":"abc1234","url":"https://github.com/you/macro_exchange/commit/abc1234"},
    "sent_at":"2026-11-15T15:00:30Z"}
   ```
   `kind` — `weekly`, `event_fomc` или `event_ecb`; `files` — текущие `.md` папок агентов, пути относительно `exchange/` (без копий в `archive/` и `json/`; содержимого нет: файлы берутся из git или папки); `git` — `null`, если git выключен или изменений не было. С `WEBHOOK_SECRET` есть заголовок `X-Macro-Signature: sha256=<hex HMAC-SHA256 сырого тела>` — приёмник пересчитывает его и сравнивает (`hmac.Equal`). Ответ 2xx — доставлено; сеть, 429 и 5xx — повтор (`webhook.retries`, по умолчанию 3, `0` — без повторов; паузы 2 → 4 → 8 с); другой код — без повтора. Ошибка webhook не ломает экспорт: строка «⚠ webhook не доставлен» в Telegram. `doctor` проверяет только адрес (тестовый запрос был бы ложной доставкой).

Порядок доставки: файлы уже в `exchange/` (папка — всегда) → git → webhook → Telegram (в сообщении — итог git и webhook). Включается каждый своим флагом: `git.enabled`, `webhook.enabled`, `notify.telegram_bot`.

**На Mac** (папки для Claude Projects):
```bash
git clone git@github.com:you/macro_exchange.git ~/MacroExchange   # любая ваша папка
```
- **Автообновление каждые 15 мин.** launchd не запускает скрипты из `~/Desktop`, `~/Documents`, `~/Downloads` (защита macOS, ошибка `Operation not permitted`), поэтому скрипт сначала копируется в `~/.local/bin`: `install -m 755 deploy/mac/pull.sh ~/.local/bin/macro-exchange-pull.sh`. Затем скопируйте `deploy/mac/com.macro.exchange-pull.plist.example` в `~/Library/LaunchAgents/com.macro.exchange-pull.plist`, замените `REPLACE_HOME` (домашняя папка) и `REPLACE_EXCHANGE` (клон) и выполните `launchctl load ~/Library/LaunchAgents/com.macro.exchange-pull.plist`. Лог — `/tmp/macro-exchange-pull.log`. Клону без доступа к вашему GitHub-аккаунту нужен отдельный deploy key только для чтения (`git -C <клон> config core.sshCommand "ssh -i ~/.ssh/macro_exchange_read -o IdentitiesOnly=yes"`).
- **Claude Projects.** У каждого агента своя папка: `usa/monetary/`, `usa/fiscal_liquidity/`, `usa/micro/`, `usa/news/`, `usa/master/`. Её файлы загружаются в knowledge соответствующего проекта. Имена файлов постоянные (D9): каждую неделю старый файл в проекте заменяется новым с тем же именем. История — в `archive/<неделя>/` и в git.
- **Бэкап БД вне сервера.** `deploy/mac/fetch-backup.sh` по ssh забирает самый свежий `backups/macro-*.db` с сервера и хранит 3 последних (`KEEP`). Ключ от сервера — `DEPLOY_KEY`, папка бэкапов на сервере — `REMOTE_BACKUP_DIR`. Еженедельный запуск — `deploy/mac/com.macro.backup-fetch.plist.example` (понедельник 09:00; скрипт — тоже в `~/.local/bin`; замените `REPLACE_HOME`, `REPLACE_BACKUPS`, `REPLACE_HOST`, `REPLACE_KEY`, `REPLACE_REMOTE`; папку бэкапов держите вне `~/Desktop`).

На сервере демон сам делает ежедневный бэкап `VACUUM INTO` в `<data_dir>/backups/macro-YYYY-MM-DD.db` (`backup.cron`, хранит `backup.keep` = 7). Файловые логи ротируются по дням (`<app>-YYYY-MM-DD.log`, 7 дней), консольный лог Docker ограничен 3 × 10 МБ.

### Деплой на сервер (Oracle Cloud Always Free, ARM, Ubuntu)

1. **VM.** Ampere A1 (`VM.Standard.A1.Flex`), Ubuntu 24.04, **только в home region** аккаунта (выбирается при регистрации). Документация Oracle (docs.oracle.com/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm, проверено 2026-11-06):
   - **Лимиты:** всего 2 OCPU и 12 ГБ памяти на все A1-инстансы; 200 ГБ дисков на boot и block volumes вместе (boot — от 47–50 ГБ); исходящий трафик — 10 ТБ/мес. Демону хватает 1 OCPU / 6 ГБ.
   - **Отзыв простаивающих:** Oracle может забрать Always Free инстанс, если за 7 дней одновременно CPU (95-й перцентиль) < 20 %, сеть < 20 % и память < 20 % (последнее — для A1). Наш демон почти всё время простаивает, так что риск реальный. Документация не говорит, касается ли это аккаунтов Pay As You Go и как отзыва избежать. Решение — за владельцем.
   - **«Out of host capacity»:** временно нет свободных A1 в home region. Пробовать другой availability domain или повторить позже; Oracle также предлагает перейти на Pay As You Go.
2. **Docker** — по docs.docker.com/engine/install/ubuntu (apt-репозиторий Docker, пакеты `docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin`), затем `sudo usermod -aG docker $USER` и перелогиниться.
3. **Код** (приватный репо) — через отдельный **read-only deploy key** репозитория с кодом. Он не тот, что для `macro_exchange`, и в контейнер не попадает:
   ```bash
   ssh-keygen -t ed25519 -N "" -C macro-code -f ~/.ssh/macro_code
   # ~/.ssh/config:
   #   Host github-code
   #     HostName github.com
   #     User git
   #     IdentityFile ~/.ssh/macro_code
   #     IdentitiesOnly yes
   git clone github-code:you/macro-collector.git ~/macro-collector
   ```
   Публичную часть `~/.ssh/macro_code.pub` добавьте в репо кода как deploy key **без** права записи.
4. **Файлы с Mac** (scp): `config.yaml`, `.env`, `secrets/deploy_key` (ключ записи в `macro_exchange`). Также свежая копия БД, снятая без остановки демона: `sqlite3 data/macro.db ".backup /tmp/macro.db"`. На сервере:
   ```bash
   cd ~/macro-collector && mkdir -p data secrets
   mv ~/macro.db data/ && mv ~/deploy_key secrets/
   sudo chown -R 10001:10001 data secrets && chmod 600 secrets/deploy_key
   docker compose up -d && make doctor
   ```
5. После этого локальный демон на Mac останавливается (`make down`), чтобы в один репо не пушили два демона.
6. **Обновление:** `cd ~/macro-collector && git pull && docker compose up -d --build`.

Логи: JSON в `<data_dir>/logs/<бинарник>.log` и текст в консоль; у каждой записи есть поле `source`.

## Регионы и агенты

Экспорт собирается из секции `regions:` конфига (D19): регион — это папка в `exchange/` и `reports/`, агент — папка с файлами, файл — один из готовых типов отчёта. Новый регион из существующих типов делается только конфигом и шаблонами, без Go-кода. Новая форма таблицы с другими данными требует Go-кода.

```yaml
regions:
  - id: usa               # строчные буквы, цифры, _ и -
    index: "^NDX"
    yahoo_poll: "22:30"   # часовой пояс конфига
    agents:
      - id: monetary      # agent в шапке, reports/<регион>/<неделя>/<id>.md
        folder: monetary  # по умолчанию = id
        series_tags: [usa_monetary]   # значения поля agent: у рядов
        texts: [fed]      # fed, treasury
        reports:
          - report: monetary
            file: current_week.md
            title: "USA Monetary"
            tables: { growth: [GDPC1], sentiment: [], labor: [], inflation: [], expectations: [], fed: [], energy: [] }
```

| Тип отчёта (`report:`) | Что задаётся в конфиге |
|---|---|
| `monetary` | таблицы `growth`, `sentiment`, `labor`, `inflation`, `expectations`, `fed`, `energy`; необязательная `credit` (деньги и кредит, у Европы) |
| `fiscal_liquidity` | таблицы `liquidity`, `funding`, `budget`, `curve`, `derived` |
| `fiscal_bonds` | таблицы `curve`, `spreads`, `monthly`, `debt`; аукционы — из секции `finanzagentur`, если её `agent` — агент региона |
| `markets` | таблица `rows`; контракты `cftc.contracts` с `agent` из `series_tags` агента — раздел COT |
| `key_metrics` | строки `rows` (`label`, `id`, `code`); `sentiment_rows: true` — ещё VIX/VIX3M, put/call, RSP/SPY, P(рецессия) |
| `calendar` | `previous`: релиз FRED → ряд, значение которого показать как прошлое; события `release_calendars` — своё `previous` |
| `news` | `filtered_file`, `uncategorized_file`, `shared` (общий файл всех регионов), `own_tag_prefix` или `own_tag_prefixes`, `other_tag_prefix`, `tag_topics` (темы: fed, fiscal, politics, energy, earnings, europe) |
| `micro` | у агента `companies` — тикеры из `edgar.companies` |
| `micro_annual` | у агента `companies` — тикеры из секции `issuers` (EU Micro: цены, даты отчётов, годовые и квартальные цифры, пресс-релизы) |
| `sentiment` | `file`, `title`; необязательный блок `sentiment:` той же формы, что верхний `sentiment` (без него — верхний, файл США): `sections` (reaction, moves, regime, volatility, breadth, positioning, predictions), `regime: { equity, rates }`, `volatility`, `vol_ratio: { num, den, label, inversion }`, `put_call`, у `reaction` — `close`, `timezone`, у события — `region` (событие другого региона), у инструмента реакции и дневных движений — `series` вместо `symbol` (ряд без минутных баров: изменение за день). Шаблон Европы — `sentiment_eu` |
| `fomc_diff`, `ecb_diff` | `file`, `title`; у любого отчёта необязательный `max_chars` — свой лимит размера вместо `export.max_file_chars` (у `ecb_diff` Европы — 100 000: заявление и Q&A ЕЦБ длиннее; тексты не режутся, превышение — предупреждение в логе) |

Ряд в таблице или строке называется по `id`; если один `id` есть у двух источников, конфиг не загрузится — дайте рядам разные `id`. Полная запись `источник/id` (`fred/DGS10`, `derived/UST_2S10S`, `yahoo/^NDX`) тоже принимается. У ряда можно указать `quality: proxy` (значение — замена показателя, а не он сам) или `quality: manual` (ручной ввод); по умолчанию `official`. В md у такой строки появляется пометка «прокси» или «ручной ввод», в JSON — поле `quality`. У производного ряда (`derived`) можно задать `label` — имя в файлах.

Конфиг проверяется при загрузке: опечатка в поле или имени таблицы, пропущенная таблица (пустая пишется `[]`) и ряд, которого нет в конфиге, — ошибка с указанием места. У агента `channels` сужает новости до перечисленных каналов и лент; пусто — все источники с этим `region`.

**Шаблоны** встроены в бинарник (`templates/*.tmpl`). Папка `templates_dir` (путь от файла конфига) заменяет шаблоны с тем же именем `{{define}}` и добавляет новые; другой шаблон у отчёта задаётся полем `template:`. Шаблон получает те же данные, что JSON. Ошибки шаблонов показывают запуск `macrod` и `macroctl doctor`.

**Пресеты:** `us-equities` (то же, что `config.example.yaml`), `eu-equities` (Европа, отдельно от США) и `rates-only` (только ставки: FRED и календарь). Оба региона в одном конфиге — раздел «Европа» ниже.

```bash
./bin/macroctl init --preset rates-only   # пишет config.yaml; существующий файл — только с --force
```

**Миграция старого конфига** (нужна один раз при выкладке этой версии: без секции `regions:` демон не стартует и пишет об этом):

1. Вставить в `config.yaml` блок `regions:` из `presets/us-equities.yaml` и список `classify.tags`.
2. Удалить `yahoo.daily_poll_us` и `yahoo.daily_poll_eu` (время теперь в `regions[].yahoo_poll`).
3. Закомментировать строки `yahoo.symbols` и `derived` с `agent: eu_*` до фазы 11.
4. (фаза 11) У отчёта `key_metrics` США добавить `sentiment_rows: true` — без него из `key_metrics_usa.md` пропадут VIX/VIX3M, put/call, RSP/SPY и P(рецессия). Блок `regions:` из `presets/us-equities.yaml` уже содержит это поле.

## Европа

Регион `eu` (фаза 11, §18.7): ЕЦБ, Бундесбанк, Eurostat, STOXX, ZEW, ручной ввод Ifo, Finanzagentur, тексты и день ЕЦБ, EU Micro, общий с США файл новостей и свой sentiment. Отдельно — `presets/eu-equities.yaml`; вместе с США — один конфиг, образец: `testdata/config/us_eu.yaml` (его загрузку и экспорт обоих регионов проверяет тест).

**Миграция `config.yaml` США → США + Европа** (сначала копия: `cp config.yaml config.yaml.bak-$(date +%F)`):

1. `regions:` — добавить регион `eu` целиком из `presets/eu-equities.yaml`; у обоих регионов — `title` («США», «Европа»: строка сообщения доставки) и `timezone` (у США не нужен: `calendar.release_timezone`; у Европы — `Europe/Berlin`, `timezone_label: "CET/CEST"`).
2. У отчёта `key_metrics` США — `sentiment_rows: true` (см. выше).
3. Новости общие: у отчёта `news` США — `shared: true`, `own_tag_prefixes: ["usa_", "eu_"]`, строку `other_tag_prefix` удалить; в `tag_topics` — `eu_ecb: europe, eu_fiscal: europe, eu_macro: europe, eu_micro: europe`. Агент `news` Европы — тот же блок отчёта, отличается `title`.
4. `cftc.contracts`: у Euro FX (`099741`) — `agent: eu_master` (COT уходит в `markets_eu.md`, из USA Sentiment пропадает).
5. Добавить секции Европы из `presets/eu-equities.yaml` целиком: `ecb`, `bundesbank`, `eurostat`, `stoxx`, `zew`, `manual`, `finanzagentur`, `release_calendars`, `ecb_texts`, `issuers`. Старую заглушку `ecb_texts: { feeds: "[[LOOKUP]]" }` и `eu_companies:` — удалить.
6. Списки, общие с США, — дописать строки Европы без повторов: `yahoo.symbols` (кроме `^NDX` и `^VIX` — они уже есть), `derived` (BTP_BUND, OAT_BUND, UST_BUND, BUND_2S10S, ECB_ALL_AAA_10Y, GER40_NDX; закомментированные раньше строки `agent: eu_*` — раскомментировать или заменить), `fred.series` (DGS10 уже есть), `dashboard.demo_allow` (ряды ЕЦБ, Bund, Eurostat).
7. `git.commit_message` — с `{region}`: `"export {iso_week} {region} {kind}"` (у каждого региона свой коммит).
8. Проверка: `macroctl doctor`, затем `macroctl export --region all --dry-run` (на копии БД — `--data-dir`).

Секции `classify`, `news`, `telegram`, `rss_feeds`, `sentiment`, `edgar` остаются от США: каналы и ленты общие, Европа читает те же посты.

## Дашборд

Графики для человека (D20, D24). Только чтение: `macroctl serve` и `macroctl site` не пишут в базу и работают отдельно от демона.

```bash
make serve    # http://127.0.0.1:8080, локально без пароля
```

Страницы задаются в конфиге, секция `regions[].dashboard.pages`: графики (`kind: line | bar | curve`, ряды — `source/id`, необязательный `transform`) и готовые блоки (`views`). Опечатка в ряду или блоке — ошибка при запуске. У региона без `dashboard` — общие страницы: обзор, календарь, отчёты. Для уже работающего конфига скопируйте блоки `dashboard` из `presets/us-equities.yaml`. Блок `issuers` (Европа, вкладка Micro) читает отчёт `micro_annual`: цены, реакции, ближайшие отчёты, годовые и квартальные цифры, релизы; в `--demo` закрыт плашкой (данные эмитентов).

**На сервере** порт открыт только на 127.0.0.1. В `.env` задайте `MACRO_DASHBOARD_PASSWORD` (логин — `MACRO_DASHBOARD_USER`, по умолчанию `macro`), запустите сервис и откройте туннель:

```bash
docker compose --profile dashboard up -d
```

```bash
ssh -L 8080:127.0.0.1:8080 user@server
```

После этого дашборд открывается на http://127.0.0.1:8080. Адрес не на loopback без пароля `serve` не примет.

**Статический сайт:** `macroctl site --out site` — полная копия для себя; `macroctl site --out site --demo` (`make site`) — для публикации. Сайт открывается двойным кликом по `site/index.html` или с GitHub Pages. Папка `site/` перезаписывается целиком, в чужую непустую папку команда не пишет.

В `--demo` попадают только ряды из `dashboard.demo_allow`, факты EDGAR, данные Treasury и официальные календари. Цены Yahoo, ICE, S&P, UMich, Telegram и файлы отчётов закрыты плашкой (D21, D24). Прежде чем добавить ряд FRED в `demo_allow`, проверьте его тег copyright: `/fred/series/tags`.

Европа: открыты ЕЦБ и собственные ряды Бундесбанка (условия ESCB: бесплатно, с указанием источника, без изменения данных; данные третьих лиц — нет, поэтому заказы и промпроизводство из базы Бундесбанка — данные Destatis — закрыты) и Eurostat (CC BY 4.0, с указанием источника). STOXX, ZEW, ручной ввод (Ifo), Yahoo и данные эмитентов (EU Micro) — под плашкой.

**Публичный демо-сайт на GitHub Pages (публикуется сам раз в неделю).** После недельного экспорта всех регионов демон собирает `site --demo` в `site.dir` (по умолчанию `data/site`; это рабочая копия репозитория сайта, её `.git` при пересборке сохраняется) и проверяет сборку: `meta.demo = true`, нет страницы состояния, каждый файл ряда — из `dashboard.demo_allow`. Затем коммит `site {iso_week}` и push в `site.repo`; при сбое — повторы, затем раз в час. В Telegram приходит строка с адресом сайта или с ошибкой. Экспорт и доставку в `macro_exchange` публикация не задевает. Если проверка не прошла, push не делается.

Настройка (один раз):
1. Создайте на GitHub **публичный** пустой репозиторий, например `macro-site`.
2. На сервере сгенерируйте отдельный ключ — не ключ `macro_exchange`:
   ```bash
   cd ~/macrod && ssh-keygen -t ed25519 -N "" -f secrets/site_key && sudo chown 10001:10001 secrets/site_key* && sudo chmod 600 secrets/site_key
   ```
   Публичную часть (`secrets/site_key.pub`) добавьте в репозиторий: Settings → Deploy keys → Add deploy key → **Allow write access**.
3. В `config.yaml`: `site.enabled: true`, `site.repo: "<owner>/macro-site"`; `docker compose up -d` (переменная `SITE_SSH_KEY_PATH` уже задана в `docker-compose.yml`).
4. Первая публикация вручную: `docker compose exec -T macrod macroctl --config /app/config.yaml site --publish`.
5. Settings → Pages → Source: «Deploy from a branch» → `main`, папка `/ (root)` → Save. Адрес: `https://<owner>.github.io/macro-site/`.
   GitHub собирает Pages только по push, сделанному после включения. Если Pages включили уже после первой публикации, сайт отдаёт 404 до следующего push: запустите `site --publish` после изменения данных или дождитесь воскресного экспорта. Если сайт нужен сразу — сделайте пустой коммит в рабочей копии `site.dir` и запушите его.

`doctor` проверяет ключ и доступ к репозиторию сайта (`site remote`).

## CLI `macroctl`

```
macroctl [--config config.yaml] [--env .env] <команда>
  backfill --source fred|treasury|yahoo|cftc|telegram|rss|edgar [--since 2000-01-01]  # загрузка истории
  fetch --source fred|fed|treasury|yahoo|cftc|telegram|rss|posts|edgar|all   # инкрементальная загрузка (fed — тексты ФРС, posts — каналы и ленты)
  fetch|backfill --source ecb|bundesbank|eurostat|stoxx|zew                 # числовые источники Европы (фаза 11)
  fetch --source ecb_texts                      # тексты ЕЦБ: решения, заявления и Q&A, accounts, речи, интервью
  input <ряд> <значение> --period YYYY-MM       # ручной ввод (Ifo), source=manual; то же — ответом боту (фаза 14c)
  issuers file [TICKER] <файл.xlsx> [--period "Q3 2026"]  # квартальный Excel эмитента, который не скачать (Allianz; фаза 14c)
  classify [--reprocess [--since 2026-10-01]] [--keywords-only]  # разметить посты тегами
  source-check [--days 30] tg:<канал> | rss:<url>   # проверить канал/ленту до добавления (ничего не пишет)
  calendar [--days 14] [--refresh] [--all]      # ближайшие релизы
  init --preset <name> [--force]                       # копирует пресет в config.yaml
  export [--region <id>|all] [--week 2026-W43] [--dry-run]  # недельный экспорт
  diff --type fomc|ecb [--latest | --a <id> --b <id>]  # изменения statement FOMC; ЕЦБ — решение и вступление заявления
  status [--all]                                # состояние источников и рядов
  guidance list [--all] | approve <id> | reject <id> | scan [--ticker GOOGL] [--dry-run]  # кандидаты capex guidance (фаза 14b)
  record-fixtures [--source fred|treasury|yahoo|cftc|gemini|edgar|ecb|ecb_texts|bundesbank|eurostat|stoxx|zew]  # перезаписать testdata/<source> ответами (нужна сеть)
```

### Загрузка FRED

```bash
make backfill                                            # вся история с 2000-01-01 (~1,5 мин, 59 рядов)
./bin/macroctl --config config.yaml fetch --source fred  # инкрементально
```

- `backfill` грузит все ряды заново; повторный запуск не создаёт дублей (upsert), изменённые значения попадают в таблицу `revisions`.
- `fetch` сначала спрашивает у FRED `last_updated` ряда: не изменился — ряд пропускается; изменился — перечитывается окно ревизий (1 год для дневных/недельных, 5 лет для месячных/квартальных).
- FRED ограничивает 120 запросами в минуту — клиент делает не больше 2 запросов в секунду и повторяет запрос при сетевых ошибках, 429 и 5xx.
- Если единицы ряда в FRED не совпадают с ряд не загружается и отмечается ошибкой — это сигнал проверить расчёты.
- Ошибка одного ряда не останавливает остальные; команда завершается с кодом 1 и печатает список упавших рядов.

После каждого `backfill`/`fetch` пересчитываются производные ряды (`source='derived'`): Net Liquidity (млрд $), U/V, SOFR−IORB, 2s10s, 3m10y (б.п.).

### Treasury, Yahoo, CFTC

```bash
./bin/macroctl --config config.yaml backfill --source treasury   # ~1,5 мин
./bin/macroctl --config config.yaml backfill --source yahoo      # ~25 с
./bin/macroctl --config config.yaml backfill --source cftc       # ~1 с
./bin/macroctl --config config.yaml fetch --source all           # всё инкрементально
```

- **Treasury** (ключ не нужен): из Fiscal Data — TGA по дням (DTS, млн $), Monthly Treasury Statement (доходы, расходы, дефицит, в долларах; новый выпуск перезаписывает месяцы — ревизии), байбэки; из TreasuryDirect — результаты аукционов (high yield / ставка bills, BTC, доли indirect / direct / dealers; в Fiscal Data результаты появляются только к дате выпуска, а в TreasuryDirect — через минуты после закрытия) и предстоящие аукционы; с home.treasury.gov — документы Quarterly Refunding (borrowing estimates и refunding statement) и даты следующих.
- **Аукционы в экспорте:** «≈tail» = high yield минус доходность CMT того же срока (FRED DGS2…DGS30) на закрытие дня аукциона — это **приближение**: настоящий tail считается к when-issued в 13:00, а WI-доходность бесплатно не публикуется. Плюс отклонения BTC, ставки и долей участников от среднего 6 предыдущих аукционов того же срока.
- **Yahoo** (неофициальный, некритичный): символы США из `yahoo.symbols` и месячные фьючерсы на ставку ФРС `ZQ<месяц><год>.CBT` на 12 месяцев вперёд. Сохраняются закрытые дневные бары; незакрытая сессия, нулевые цены и однодневные всплески > 50 % с возвратом отбрасываются (WARN в логе). Если Yahoo по символу упал или устарел, в экспорте берётся запасной ряд FRED (`fallback_for`) с флагом `fallback`.
- **Траектория ставки (FED_PATH)** считается при экспорте: для каждого заседания FOMC в ближайшие 12 месяцев — контракт следующего месяца, implied = 100 − цена, разница с DFF в б.п. и число шагов по 25 б.п. к последнему контракту («снижения» или «повышения»). Даты заседаний на год вперёд обновляются вместе с календарём.
- **CFTC COT** (отчёт TFF, Futures Only): long/short Leveraged Funds и Asset Managers и open interest по `cftc.contracts`; нетто и z-score за 52 недели считаются при экспорте.

### Европа: источники и EU Micro

Пресет `presets/eu-equities.yaml` (регион `eu`, слияние с США — раздел «Европа» выше). Ключи API не нужны.

```bash
D=$(mktemp -d); sqlite3 data/macro.db ".backup '$D/macro.db'"      # опыты — на копии БД
./bin/macroctl --config presets/eu-equities.yaml --data-dir "$D" backfill --source ecb   # ecb|bundesbank|eurostat|stoxx|zew
./bin/macroctl --config presets/eu-equities.yaml --data-dir "$D" fetch --source yahoo    # индексы, EUR/USD, Brent, TTF, доходности MTS
./bin/macroctl --config presets/eu-equities.yaml --data-dir "$D" input IFO_CLIMATE 88.4 --period 2026-10
./bin/macroctl --config presets/eu-equities.yaml --data-dir "$D" fetch --source ecb_texts   # тексты ЕЦБ
./bin/macroctl --config presets/eu-equities.yaml --data-dir "$D" diff --type ecb --latest   # решение и заявление против прошлого заседания
./bin/macroctl --config presets/eu-equities.yaml --data-dir "$D" export --region eu --no-deliver
```

- **ecb** — ставки (DFR, €STR), баланс Евросистемы, HICP, кривые доходностей; Data Portal часто отвечает 504 — клиент повторяет сам.
- **bundesbank** — Bund 2Y/10Y/30Y по дням, заказы и производство Германии.
- **eurostat** — ВВП еврозоны и Германии (`qoq` — кв/кв), долг и дефицит, опрос Еврокомиссии (industrial confidence, ESI).
- **stoxx** — VDAX-NEW и VSTOXX; **zew** — ZEW Sentiment и Situation (файл xls; между запросами к zew.de — пауза 15 с).
- **Спреды** (`derived`): BTP-Bund, OAT-Bund (Euronext MTS; без `MDE10.AS` — нога Bund 10Y Бундесбанка с флагом), UST-Bund, 2s10s Bund, GER40/NDX.
- **Тексты ЕЦБ** (секция `ecb_texts`, батч 11c) — RSS `ecb.europa.eu/rss/press.html` (15 последних: речи и интервью членов Исполнительного совета, решения, заявления) и годовые списки решений, заявлений и accounts (`{year}` в адресе); прочие пресс-релизы не берутся. Заявление пресс-конференции хранится двумя документами: вступление (`ecb_statement`, с ним сравнивается следующее) и Q&A (`ecb_press_conference`, id `…#qa`), который ЕЦБ дописывает на ту же страницу через час — его забирает перечитывание заявлений последних 21 дня; поправка ЕЦБ во вступлении — ревизия того же документа, в логе предупреждение. Ставки из решения разбираются по известным формулировкам; не распознано — в файле «не распознано», без чисел; распознанная DFR после вступления в силу сверяется с рядом `check_series` (`ECB_DFR`), расхождение — предупреждение в логе.
- **Календарь заседаний ЕЦБ** — страница `mgcgc`: день заседания с пресс-конференцией (любой город) даёт события «ECB decision» 14:15 и «ECB press conference» 14:45 Europe/Berlin на год вперёд.
- **День решения ЕЦБ** (`ecb_texts.day_mode`): с 14:14 RSS каждые 20 с, годовые списки — раз в минуту; найдено решение — экспорт ждёт заявление до 15:30 и уходит один раз (`eu/monetary_macro/ecb_diff.md`, `kind: event_ecb`): решение и ставки против прошлого заседания, diff решения, diff вступления, Q&A (если уже есть). Нет решения к 14:25 — тревога в Telegram; нет ни решения, ни заявления к 15:30 — вторая тревога, экспорта нет (каждая — один раз, переживает перезапуск). Недельный экспорт перерисовывает `ecb_diff.md`; в неделю без заседания первая строка — «⚠ Последнее заседание … Следующее …». До батча 11d в папке `monetary_macro` только `ecb_diff.md`.
  - Режим дня не сработал — вручную: `macroctl fetch --source ecb_texts`, затем `macroctl export --region eu`; недельный экспорт всё равно подберёт документы.
  - Старые конфиги содержат заглушку `ecb_texts: { feeds: "[[LOOKUP]]", … }`: она игнорируется, при старте в логе предупреждение «ecb_texts.feeds — устаревший ключ»; удалите блок или замените секцией из `presets/eu-equities.yaml`.
- **Календари релизов** (`release_calendars`): у календаря необязательный `horizon_days` — хранить его события дальше общего `calendar.horizon_days`; Eurostat и статкалендарь ЕЦБ — 120 дней, чтобы у квартальных рядов был известен следующий релиз (иначе флаг `stale` ставится по общему правилу частоты, а лаг европейских данных о труде — до 2,5 мес.).
- **SPF на следующий год** (ряд `SPF_NEXT_YEAR`): в ключе ЕЦБ `{next_year}` на месте измерения года — один запрос по всем годам, для каждого квартала опроса берётся прогноз на следующий календарный год. Выпуски SPF — в `calendar_eu.md` (календарь `ecb_spf`, даты дописываются по мере объявления ЕЦБ).
- **Ряды, опубликованные вперёд** (`forecast: true` у ряда, сейчас — wage tracker ЕЦБ): в отчёте — значение на конец недели как факт и пометка «прогноз на <месяц>: …» (последняя точка); z, изменения и `stale` считаются только по факту. На дашборде у линии графика `part: actual` (до даты данных) или `part: forecast` (после неё) — два графика из одного ряда.
- **EU Micro** (секция `issuers`, агент `micro`, отчёт `micro_annual`): цены и реакция на отчёт по 8 компаниям, даты отчётов — вручную из календарей эмитентов (`estimate: true` — не подтверждена эмитентом), у DT, Siemens, Siemens Energy и Rheinmetall ещё со страницы календаря (`calendar`, см. ниже); годовые цифры SAP — EDGAR 20-F (`ifrs-full`, контакт и адрес — из секции `edgar`, даже выключенной), Airbus — ESEF (filings.xbrl.org); квартальные цифры Siemens и Deutsche Telekom — из Excel эмитента (`quarterly`: таблица по строке-заголовку, строка по подписи, столбец по заголовку периода; не нашлось ровно один раз — ошибка, файл не пишется); пресс-релизы — RSS и HTML-списки, в файл идут только абзацы об outlook и цитаты. `macroctl fetch --source issuers`; цены — с `yahoo`. Allianz: сайт закрыт (403) — квартальный Financial supplement присылает пользователь (`quarterly.source: file`, см. ниже; структура проверена на Q1–Q3 2026: total business volume вместо выручки, operating profit, прибыль акционерам, базовая EPS).
- **Даты отчётов со страницы календаря** (`issuers.companies[].calendar`, фаза 14c): у эмитентов нет `.ics`/RSS, поэтому страница читается в `issuers.poll_cron`: `kind: json_attr` — JSON `{title, startDate}` в атрибуте (DT), `kind: text` — строки, начинающиеся с даты (`Nov. 12, 2026`, `February 11, 2027`, `07/05/2026` при `slash: dmy`). Берутся только события, совпавшие с `events[].match` (группы `q`, `year`), имя периода — `events[].period` (`Q{q} FY{year}`). Ничего не совпало — ошибка в `fetch_state` (`issuer_calendar:<тикер>`), старые даты остаются. Слияние с `report_dates`: ручная дата побеждает (другая дата в календаре — пометкой в источнике), дата календаря заменяет `estimate` и добавляет недостающие периоды; в источнике — «(автоматически)». Перенос даты — в логе. У SAP, Mercedes, Allianz, Airbus страница закрыта защитой от ботов — даты вручную.
- **Квартальный файл от пользователя** (`quarterly.source: file`, без `page`; фаза 14c): файл `.xlsx` документом в чат бота, подпись «ALV Q3 2026» (тикер не нужен, если такой эмитент один; квартал — если не виден из имени по `quarterly.links`), или `macroctl issuers file ALV fs.xlsx --period "Q3 2026"`. Разбор тот же, что у скачанных файлов; не нашлось ровно один раз — ничего не пишется, ответ с ошибкой. Бот скачивает файлы до 20 МБ. **Напоминание:** с дня отчёта (из `report_dates` и календаря) раз в будний день (в `issuers.poll_cron`) 21 день, пока файла этого квартала нет, бот пишет «<Эмитент>: отчёт за Q3 2026 вышел … — пришлите квартальный Excel» — как отправить (📎 → «Файл»), готовая подпись `ALV Q3 2026`, шаблон `<ТИКЕР> Q<квартал> <год>`, где взять (`quarterly.hint`), команда `macroctl` на случай без Telegram. Ошибка в подписи — ответ с тем же шаблоном.
- **`macroctl input`** — ручной ввод рядов секции `manual` (Ifo: сайт закрыт для программ). Период — в частоте ряда, будущий нельзя; повтор с другим значением — ревизия; в отчёте пометка «ручной ввод». Даты выхода — `release_dates` ряда: после дня выхода без нового значения ряд помечается `stale`.
- **Ifo через бота** (`manual.reminder`, фаза 14c; нужен `notify.telegram_bot`): по будням в `time` (10:05 Europe/Berlin) бот присылает «Ifo за <месяц> — введите значения» с шаблоном ответа (порядок чисел, «Шаблон: <1> <2>», пример, запасной вид `ifo … ГГГГ-ММ`, где взять — `hint`), пока значения за текущий месяц нет хотя бы у одного ряда — с первой даты месяца из `release_dates`, а если дат на месяц нет, с `from_day` (20-е). Не больше одного напоминания в день. Ответ — reply на напоминание: `88.4 91.2` (порядок `series`, запятая допустима) или в любой момент `ifo 88.4 91.2 [2026-10]`. Значения пишутся как через `macroctl input`; бот отвечает, что записано (новое / ревизия / без изменений). Неверное число или количество — подсказка, ничего не пишется. **Даты выхода через бота:** `ifo даты 24.11.2026 17.12.2026` (или `2026-11-24`), `ifo даты -24.11.2026` — убрать ошибочную, `ifo даты` — список будущих. Хранятся в БД (`manual_release_dates`) и действуют вместе с `release_dates` конфига: напоминание — в день выхода, `stale` — от него. Если дат впереди нет, напоминание просит их прислать.

### Недельный экспорт

```bash
./bin/macroctl --config config.yaml export --week 2026-W43 --dry-run   # показать, что будет создано
./bin/macroctl --config config.yaml export --week 2026-W43             # записать
make export-dry                                                         # прошлая неделя, без записи
```

Неделя — с воскресенья 16:00 до воскресенья 16:00 (Europe/Bratislava), без `--week` — последняя закончившаяся. Файлы в `<data_dir>/exchange/` (формат — `examples/`):

| Папка → Claude Project | Файлы |
|---|---|
| `usa/monetary/` → USA Monetary | `current_week.md` (+ `fomc_diff.md` после заседания) |
| `usa/fiscal_liquidity/` → USA Fiscal & Liquidity | `current_week.md` |
| `usa/micro/` → USA Micro | `current_week.md` |
| `usa/news/` → USA News | `current_week.md` |
| `usa/master/` → USA Master | `markets_usa.md`, `calendar_usa.md`, `key_metrics_usa.md`, `forecast_scorecard.md` (фаза 13) |

Те же данные для машин (дашборд, API) — в `exchange/json/<путь файла>.json`, в архиве — `archive/<iso_week>/json/…` (D23). **В Claude Projects загружаются только папки `usa/*`:** JSON лежит отдельным деревом, чтобы не удваивать контекст агента. Числа в JSON сырые (без округления) и с единицей: `{"value": 4.0812, "unit": "percent"}`, отсутствующее — `"value": null`; JSON Schema каждого файла — `docs/schema/*.json`, версия контракта — поле `header.schema_version`. Markdown и JSON собираются из одной Go-структуры (`internal/export`, шаблоны — `internal/export/templates/`).

Файлы перезаписываются с теми же именами, копия недели — в `exchange/archive/<iso_week>/`. Если в `<data_dir>/reports/usa/<прошлая неделя>/` лежит сохранённый ответ агента (`monetary.md`, `fiscal_liquidity.md`, `micro.md`, `news.md`, `master.md`), он копируется в папку агента как `previous_report.md`. В «Что вышло за неделю» попадают ряды, чьё новое наблюдение пришло за неделю (первичная загрузка истории релизом не считается). Подключение папок к Claude Projects и доставка на Mac через git — фаза 6.

### Промпты агентов (фаза 12)

Инструкции десяти агентов лежат в репо: `prompts/<регион>/<агент>.md` — `prompts/usa/{monetary,fiscal_liquidity,micro,news,master}.md`, `prompts/eu/{monetary_macro,fiscal_bonds,micro,news,master}.md` (и `prompts/rates/curve.md` для пресета `rates-only`). Имя файла — `id` агента из `regions[].agents[]`, как у `reports/`. В каждом: роль, файлы агента, ключевые вопросы (из `DATA_MAP.md`), правила (только числа из файлов, факты отдельно от мнений, флаги `stale`/`rev`/`proxy`/«ждём …» — «нет данных», сравнение с `previous_report.md`) и формат отчёта. Инструкции — на английском, отчёт агент пишет на русском.

- **Экспорт.** Каждый недельный экспорт кладёт промпт в папку агента как `instructions.md` (и в `archive/<неделя>/`); доставка коммитит его вместе с остальными файлами. Нет файла промпта — предупреждение в логе, старый `instructions.md` удаляется, экспорт продолжается. Битый файл — ошибка экспорта. `--dry-run` ничего не пишет и показывает «instructions.md: N of M agents».
- **Подключение к Claude Project.** Загрузите `instructions.md` в knowledge проекта вместе с остальными файлами папки (или вставьте его текст в Project instructions — тогда обновляйте вручную при смене версии).
- **Версия.** В начале файла промпта — `version: 1` (поднимайте при смысловых правках). В `instructions.md` демон пишет `prompt_version: "1 (3f9a2c1)"` — версия и 7 знаков sha256 файла, так что правка без подъёма версии тоже видна. Первая строка отчёта агента: `Неделя: 2026-W46 | Агент: USA Master | Промпт: 1 (3f9a2c1)`.
- **Мастеры** заканчивают отчёт блоком ```` ```yaml ```` `forecast:` (`instrument`, `direction: up|down|range`, `invalidation` — только число) — его разбирает лог прогнозов (ниже). Отчёт USA Master к EU Master прикладывает пользователь.
- **Правка** — без пересборки: файл читается при каждом экспорте. Папка — `export.prompts_dir` (по умолчанию `prompts`, путь от папки `config.yaml`). `macroctl doctor` проверяет, что промпт есть и разбирается у каждого агента каждого региона.

### Лог прогнозов (фаза 13)

Мастер каждой недели заканчивает отчёт блоком `forecast:` — прогноз индекса на следующую торговую неделю. Сохраните отчёт как `<data_dir>/reports/<регион>/<неделя>/master.md` — дальше всё автоматически:

- **При недельном экспорте** (`forecast.enabled: true`) демон читает все `master.md` региона, записывает прогнозы в таблицу `forecasts`, оценивает те, чья неделя закончилась, и кладёт `forecast_scorecard.md` в папку мастера (и в `archive/<неделя>/`); доставка коммитит его вместе с экспортом. Ошибка лога — предупреждение, экспорт не падает.
- **Вручную:**

```bash
./bin/macroctl --config config.yaml forecast import --region usa              # разобрать отчёты (все недели или --week 2026-W46)
./bin/macroctl --config config.yaml forecast score --region all --dry-run     # оценить и показать scorecard, не записывая
./bin/macroctl --config config.yaml forecast score                            # оценить и записать scorecard (без архива и доставки — их сделает экспорт)
```

- **Правила оценки** (D26): опорная цена — последнее дневное закрытие недели отчёта, итог — последнее закрытие следующей недели. `up` — итог выше опорной, `down` — ниже, `range` — |Δ| < `forecast.range_pct` % (по умолчанию 1). Любое дневное закрытие недели прогноза за уровнем `invalidation` (up — ниже, down — выше, range — по стороне уровня от опорной) — `invalidated`. Hit rate = hit / (hit + miss + invalidated). Неделя ещё не закончилась — `pending`; неделя прошла, а закрытий нет — строка «⚠ нет закрытий», предупреждение в логе экспорта. Оценки, чья неделя прогноза закончилась меньше 14 дней назад, пересчитываются при каждом запуске: закрытие, загруженное с опозданием (сбой пятничного опроса), исправит результат.
- **Цены** — ряд `price` инструмента (`^NDX`, `^GDAXI`); если у него нет закрытия недели — `fallback` (`NASDAQCOM`), тогда уровень не проверяется (другая шкала), в scorecard пометка.
- **Нет блока или битый блок** (`24 500`, `up | down`, лишние ключи, неизвестный инструмент) — неделя в конце scorecard с причиной, в hit rate не входит; прежние строки этой недели не трогаются. Правка отчёта и повторный экспорт/`import` пересчитывают только изменённые прогнозы.
- **Настройка** — секция `forecast`: `enabled`, `agent` (по умолчанию `master`), `range_pct`, `instruments` (`name` — как в блоке, `price`, `fallback` — настроенные ряды). Регион без агента `agent` лога не ведёт.

### Telegram, RSS и теги (USA News)

```bash
./bin/macroctl --config config.yaml backfill --source telegram   # история каналов за history_days (~1 мин на 300 постов)
./bin/macroctl --config config.yaml fetch --source posts          # новые посты всех каналов и лент
./bin/macroctl --config config.yaml classify --reprocess --since 2026-10-01   # разметить заново
```

- **Telegram** — публичные каналы через веб-превью `t.me/s/<канал>`: аккаунт и ключи не нужны. Сохраняется текст поста (без цитаты поста, на который ответили), дата, ссылка; у пересланного — ссылка на оригинал. Посты без текста и служебные сообщения пропускаются; отредактированный пост обновляется и размечается заново.
- **RSS** — любая лента RSS/Atom (Substack и т.п.): заголовок, автор, текст статьи без кнопок и картинок.
- **Теги** (`usa_monetary`, `usa_fiscal`, `usa_micro`, `eu_ecb`, `eu_fiscal`, `eu_macro`, `eu_micro`) ставит **Gemini** (`classify.gemini`, модель `gemini-3.5-flash-lite`) пакетами по 20 постов по запросу из `prompts/classify.md`; крипта — без тегов, реклама и пустые посты — мусор. Ответ проверяется; повтор того же текста берётся из кэша и квоту не тратит. Нет ключа, ошибка, квота, свой предел `daily_request_limit` (по умолчанию 200 запросов в сутки) или невалидный ответ — пакет размечается **ключевыми словами** `classify.rules` (`слово*` — любое окончание). У каждого тега в БД видно, кто его поставил.
- **Править запрос** — `prompts/classify.md`, без пересборки, вступает в силу после перезапуска демона; если модель систематически ошибается, лучше всего добавить пример в раздел «Examples». Уже размеченные посты не трогаются — для них `classify --reprocess`. Список тегов в файле должен совпадать с `classify.rules`, иначе демон не стартует.
- **Репосты** склеиваются: пересылка и оригинал, пересылки одного поста, одинаковый текст (без ссылок, эмодзи и пунктуации, от 8 слов) в пределах 7 дней. В экспорт идёт самый ранний пост со строкой «Также в: @канал».
- **USA News** (`usa/news/current_week.md`, в недельном экспорте): посты каналов с `region: usa` — обзор (темы недели, топ-5 по просмотрам, доли каналов с ⚠ при >50%), сюрпризы недели (ожидание из поста, факт из FRED, где есть), мнения и новости по 8 темам полным текстом, счётчики отфильтрованного. Шум (анонсы, подписи к картинкам, продуктовые новости, мусор, дубли) убирается целиком; список — `exchange/archive/<неделя>/news_filtered.md`, посты без тегов — `uncategorized.md`. Больше 60 000 символов — убираются новостные посты с наименьшим числом просмотров; мнения и тексты не режутся. Топ и порядок мнений — по резонансу (просмотры / медиана канала за 4 недели); мнений одного канала целиком — не больше `news.opinions_per_channel`, остальные строкой со ссылкой; у каналов — `profile` в обзоре. Тема — голосованием слов (тег классификатора — подсказка). Стоп-слова и слова тем — `news.drop_keywords`, `news.topics` в `config.yaml`. Флаг фото (для фильтра подписей) у постов прошлых недель: `macroctl fetch --source views --days 11`.
- Проверка качества без сети: `go test ./internal/classify -run Labeled -v` (38 размеченных примеров: Gemini 36/38, ключевые слова 25/38). Перезаписать ответы Gemini: `macroctl record-fixtures --source gemini`.

#### Как добавить канал или ленту

1. Проверить источник: `./bin/macroctl --config config.yaml source-check tg:<канал>` (имя из ссылки `t.me/<канал>`) или `source-check rss:https://…/feed`. Покажет, открывается ли он, сколько постов за 30 дней, язык и последние посты; в БД ничего не пишет.
2. Добавить одну строку в `config.yaml` (и в `config.example.yaml`, чтобы не разъехались):
   ```yaml
   telegram:
     channels:
       - { handle: <канал>, region: usa, type: news, default_tag: "" }   # type: news | opinion; region: usa | eu
   rss_feeds:
     - { name: <латиница_без_пробелов>, url: "https://…/feed", region: usa, type: opinion, default_tag: "" }
   ```
   `default_tag` — тег, который каналу добавляется всегда (обычно пусто); `history_days: N` — своя глубина истории при первом опросе (по умолчанию `posts.history_days`, 30 дней).
3. Перезапустить демон (или `macroctl fetch --source posts`). Новый источник сам загрузит историю. Ошибка в строке (имя, ссылка, регион, тип, тег) — демон не стартует и пишет, в какой строке и что не так.

### Отчёты компаний: SEC EDGAR (USA Micro)

```bash
./bin/macroctl --config config.yaml calendar --refresh            # в том числе даты отчётов (Nasdaq)
./bin/macroctl --config config.yaml backfill --source edgar       # отчёты и факты с edgar.history_start (~30 с)
./bin/macroctl --config config.yaml backfill --source yahoo       # в том числе дневные цены компаний
./bin/macroctl --config config.yaml fetch --source edgar          # только новое (~1 с, если ничего не вышло)
```

- **Компании** — `edgar.companies` (NVDA, MSFT, AAPL, AMZN, META, GOOGL, AVGO, TSLA), capex — по `edgar.capex_companies` (MSFT, GOOGL, AMZN, META). Ключ не нужен, но SEC требует контакт в `edgar.user_agent` (без него отвечает 403) и не больше 10 запросов в секунду (стоит 8).
- **Числа (выручка, EPS, capex) — только из официального JSON** `data.sec.gov/api/xbrl/companyfacts`. HTML-документы 10-K/10-Q не скачиваются: новый 10-Q/10-K в списке подач — лишь сигнал перечитать факты. Факты хранятся как поданы (квартал, с начала года, год), квартал считается при экспорте: Q4 = год − 9 мес. EPS за фискальный Q4 так посчитать точно нельзя (разное число акций) — он помечен «≈».
- **Пресс-релиз** — приложение Ex-99.1 к 8-K с item 2.02. Из него берутся только текст раздела Outlook/guidance и цитаты CEO/CFO (у MSFT, AAPL, GOOGL прогноза в релизе нет — он на звонке). TSLA подаёт item 2.02 и на отчёт о поставках; такой 8-K отчётом о прибыли не считается: нужен отчёт в календаре в тот же день или 10-Q/10-K следом.
- **Даты отчётов** — Nasdaq API (у Yahoo нужен crumb); «estimated» = оценка Nasdaq по прошлым датам, в экспорте помечена. Хранятся на 100 дней вперёд; консенсус EPS, который Nasdaq тоже отдаёт, не используется — консенсус только текстом из News (решение D11).
- **Реакция цены** — по дневным ценам Yahoo: отчёт после закрытия — закрытие следующей сессии к закрытию дня отчёта; до открытия — закрытие дня отчёта к предыдущему.
- **USA Micro** (`usa/micro/current_week.md`): отчёты недели, capex гиперскейлеров за 5 кварталов с суммой (только полные кварталы), сезон отчётов (кто отчитался, кто впереди), Outlook и прошлый Outlook, цитаты, пропущенные 10-Q. Больше 60 000 символов — сначала убираются цитаты, потом Outlook обрезается со ссылкой; таблицы не режутся.

#### Capex guidance: кандидаты и подтверждение (фаза 14b)

Implied Q4 capex (§19.3a) считается по годовому guidance — только у компаний, где он определён как наш capex (`edgar.guidance_tickers`, сейчас GOOGL; тикер должен быть в `edgar.capex_companies`).

- **Поиск.** После новой расшифровки звонка или 8-K у тикера из `guidance_tickers` программа ищет предложения, где есть capex, слово-ориентир (expect, anticipate, guidance, outlook, plan, invest, range, will), сумма или диапазон в $ млрд (`$195‑$205 billion`, `$195 billion to $205 billion`, `between $195 and $205 billion`, `$195B`) и ровно один год. Сумма квартала рядом с диапазоном на год не мешает. Без года или суммы, с двумя годами, в миллионах — не кандидат.
- **Кандидат** (`guidance_candidates`) — тикер, год, диапазон, цитата, ссылка. Пока он не подтверждён, число нигде не используется. Если диапазон совпадает с действующим и документ не новее — кандидата нет. Тот же диапазон в более новом документе — кандидат «подтверждает текущий диапазон» (approve обновит дату, и «⚠ устарел» пропадёт). Другой диапазон — кандидат с «⚠ отличается от подтверждённого», до approve в расчёте старое значение.
- **Подтверждение.** Сообщение в Telegram с цитатой и кнопками ✅/❌ (при `notify.telegram_bot`; нажатия принимаются только из `TG_CHAT_ID` — числовой id чата), после нажатия текст меняется на «✅ Подтверждён …» / «❌ Отклонён …». Или вручную:
  ```bash
  ./bin/macroctl --config config.yaml guidance list              # ожидающие и действующее значение
  ./bin/macroctl --config config.yaml guidance approve 1         # или reject 1
  ./bin/macroctl --config config.yaml guidance scan --ticker GOOGL --dry-run   # поиск по уже сохранённым документам (без Telegram)
  ```
- **Экспорт.** `edgar.capex_guidance` из config.yaml и подтверждённые значения (`capex_guidance` в БД) объединяются: для тикера и года побеждает более свежий `given`. Программа конфиг не переписывает — он остаётся запасом.
- Бот один на демон (`internal/bot`, фаза 14c): тот же опрос принимает ответы Ifo и файлы эмитентов, на непонятное сообщение отвечает списком форматов; сообщения из других чатов игнорируются. Бот читает обновления долгим опросом `getUpdates` из `macrod`. Второй демон с тем же токеном (ноут и сервер одновременно) даёт 409 — в логе предупреждение, опрос реже (1 → 10 мин).

#### Как добавить компанию

1. Найти CIK в `https://www.sec.gov/files/company_tickers.json` (10 цифр с нулями слева).
2. Добавить строку в `edgar.companies` (и в `config.example.yaml`):
   ```yaml
   - { ticker: ORCL, cik: "0001341439", importance: medium }
   ```
   Если компания пишет выручку или capex другим тегом XBRL — переопределить: `concepts: { revenue: [Revenues] }` (теги видны в `https://data.sec.gov/api/xbrl/companyfacts/CIK<cik>.json`). Для суммы capex — добавить тикер в `capex_companies`.
3. `macroctl backfill --source edgar` и `macroctl backfill --source yahoo`; при следующем `calendar --refresh` появится дата её отчёта.

### Состояние

```bash
make status                                         # сводка + только проблемные ряды
./bin/macroctl --config config.yaml status --all    # все ряды
```

Показывает размер БД, по каждому источнику (FRED, Yahoo, Treasury, CFTC, derived) — время последнего успешного опроса, число ошибок, устаревших (`stale`) и ни разу не загруженных рядов; календари, ленты ФРС и датасеты Treasury (последний успех, ошибка), число документов ФРС по типам, аукционы за 7 дней (с результатами / без), последний недельный и FOMC-экспорт, события на 7 дней (ближайшее важное), события в опросе и `timeout` за 7 дней; по каждому каналу и ленте — число постов (всего и за 7 дней), последний пост, последний опрос и ошибку; разметку за 7 дней (Gemini / ключевые слова / мусор / без тегов / не размечено / дубли), запросы Gemini за сутки и задан ли ключ; таблицу рядов с последней датой наблюдения и последней ошибкой.

### Календарь релизов

```bash
./bin/macroctl --config config.yaml calendar --refresh   # обновить и показать 14 дней
make calendar                                            # показать из БД
```

- **FRED:** для каждого ряда один раз определяется его релиз (`fred/series/release`, номер хранится в `series_meta.release_id`), затем одним запросом `fred/releases/dates` берутся даты всех релизов на 14 дней вперёд. FRED даёт только дату — время выхода (ET) задаётся по номеру релиза в `calendar.release_times` (CPI/NFP/GDP/PCE/claims 08:30, JOLTS/UMich 10:00, H.15 16:15, H.4.1 16:30 и т.д.), остальные — `default_release_time` (08:30).
- **Treasury:** предстоящие аукционы (время закрытия заявок; 10/20/30Y — high), публикации DTS и MTS (календарь Fiscal Data), даты Quarterly Refunding.
- **ФРС:** с federalreserve.gov — заседания FOMC (решение в 14:00 ET второго дня, `*` = заседание с SEP), minutes (дата со страницы или решение + 21 день, 14:00 ET), Beige Book (14:00 ET).
- Если источник перенёс событие, старое получает статус `rescheduled` (`--all` показывает такие).
- Время в таблице — ET и Europe/Bratislava.

### Тексты ФРС

```bash
./bin/macroctl --config config.yaml fetch --source fed
```

Из RSS federalreserve.gov (`fed_texts.feeds`): FOMC statement, minutes, SEP (Table 1), речи и testimony членов Board (сотрудники и региональные ФРБ не собираются), плюс последний Beige Book со страницы расписания. Страница превращается в чистый текст (без меню, кнопок, сносок). Повторный опрос скачивает только новые элементы ленты.

### Diff statement FOMC

```bash
./bin/macroctl --config config.yaml diff --type fomc --latest
./bin/macroctl --config config.yaml diff --type fomc --a monetary20260729a --b monetary20260916a
```

Сравнение по предложениям: изменённые, добавленные и удалённые предложения, изменённые слова — `~~старое~~ **новое**`, затем полный новый текст с разметкой. `--a`/`--b` — номер документа в БД или имя страницы statement.

### Демон

`make run` запускает `macrod`. Он работает по календарю:

| Что | Когда |
|---|---|
| Релиз FRED | с времени выхода: каждую минуту 15 мин → каждые 5 мин до 3 ч → каждые 30 мин до 24 ч; ряды релиза загружаются, как только FRED их обновил (`done`), иначе `timeout` |
| Решение FOMC | с 13:59 ET каждые 20 с до 14:30, дальше как обычный релиз; statement сразу сравнивается с предыдущим и пишется в `<data_dir>/exchange/usa/monetary/fomc_diff.md` (+ копия в `exchange/archive/<iso_week>/`) |
| Minutes, Beige Book | как релиз FRED, до появления документа |
| Аукцион Treasury | с закрытия заявок, как релиз FRED, до появления результатов |
| DTS, MTS, Quarterly Refunding | с времени публикации, до появления данных / документа |
| Недельный экспорт | воскресенье 16:00 (`export.weekly_cron`) |
| Страховочный опрос Treasury | 06:15 |
| Отчёт компании (дата из Nasdaq) | с ожидаемого времени 8-K (после закрытия — 16:00 ET, до открытия — 07:00 ET), как релиз FRED: опрос EDGAR до 8-K и фактов 10-Q/10-K; без 10-Q к концу окна — `done`, факты подберёт страховочный опрос |
| Страховочный опрос EDGAR (все компании) | 06:30 |
| Yahoo (США) | ежедневно 22:30 (`regions[].yahoo_poll`), включая выходные: закрытие фьючерсов пятницы приходит в субботу |
| CFTC COT | пн–пт 22:00 (`cftc.check_cron`): отчёт выходит в пятницу 15:30 ET, после праздников — в понедельник |
| Обновление календаря | 05:00 (и при старте, если календарю больше суток) |
| Страховочный опрос всех рядов FRED | 06:00 (`--fetch-cron`) |
| Дневные ряды FRED без события в календаре | 23:00 |
| RSS ФРС | каждые 15 мин (`fed_texts.poll_interval`) |
| Telegram и RSS → теги → репосты | каждые 15 мин (`posts.poll_interval`; первый раз — через 15 мин после старта) |

Время cron-задач — в часовом поясе `timezone`. Задачи не накладываются друг на друга. Состояние опроса хранится в `releases.status`, поэтому после перезапуска демон продолжает опрос начатых событий; события, пропущенные пока демон был выключен дольше суток, получают `timeout`. Git push и уведомления после экспорта — фаза 6 (сейчас только запись в лог). По Ctrl+C / SIGTERM демон дожидается текущих задач и записей в БД и выходит.

Проверить день FOMC без сети — симуляция на записанных страницах 16.09.2026; недельный экспорт — golden-тест на синтетической неделе W43:

```bash
go test ./internal/scheduler -run FOMCDaySimulation -v
go test ./internal/export -run WeeklyGolden -v      # эталоны: testdata/export/weekly_*.md
```
