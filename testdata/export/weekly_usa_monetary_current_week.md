---
region: usa
agent: monetary
iso_week: 2026-W43
period: 2026-10-18 16:00 → 2026-10-25 16:00 (Europe/Bratislava)
generated_at: 2026-10-25T16:00:04+01:00
kind: weekly
sources_ok: [fred, fed]
sources_failed: [yahoo]
stale_series: []
---

# USA Monetary — 2026-W43

## 1. Что вышло за неделю

| Дата (ET) | Релиз | Показатель | Факт | Пред. | Изменение | Ревизия пред. |
|---|---|---|---|---|---|---|
| 2026-10-22 08:30 | Unemployment Insurance Weekly Claims Report | ICSA | 268 242 | 270 609 | −2 367 | 267 609 → 270 609 |
| 2026-10-22 08:30 | Unemployment Insurance Weekly Claims Report | CCSA | 2 238 135 | 2 243 077 | −4 942 | — |
| 2026-10-23 10:00 | Surveys of Consumers | UMCSENT | 76.4 | 76.4 | 0.0 | — |
| 2026-10-23 10:00 | Surveys of Consumers | MICH | 4.1% | 4.1% | 0.0 п.п. | — |
| 2026-10-24 | GDPNow | GDPNOW | 2.4% | 3.2% | −0.8 п.п. | — |

## 2. Метрики по блокам

Формат: значения — уровень на дату; `3mA`/`6mA` — динамика в годовом выражении; `z` — z-score (52 нед. для дневных/недельных, 5 лет для месячных/квартальных). Флаги: `stale` — последний релиз по календарю прошёл без новых данных (у рядов без календаря — данные старше ожидаемой частоты), `rev` — значение пересмотрено за неделю, `FRED fallback` — Yahoo недоступен, взят запасной ряд FRED.

### 2.1 Рост
| Показатель | Дата | Уровень | QoQa / MoM | YoY | 3mA | z | Флаги |
|---|---|---|---|---|---|---|---|
| Real GDP | 2026-Q2 | 30 526 млрд $ | 0.9% | 1.3% | — | — |  |
| GDPNow | 2026-Q3 | 2.4% | Δ −0.8 | — | — | — | rev |
| Nominal GDP | 2026-Q2 | 39 304 млрд $ | 0.9% | 1.0% | — | — |  |
| GDP Deflator | 2026-Q2 | 163.5 | 2.7% | 2.9% | — | — |  |
| Personal Consumption | 2026-08 | 26 783 млрд $ | 0.4% | 1.0% | 4.2% | — |  |
| Net Exports | 2026-Q2 | −1 146 млрд $ | Δ −2.8 | — | — | — |  |
| Personal Saving Rate | 2026-08 | 5.8% | Δ 0.0 | — | — | — |  |

### 2.2 Сентимент
| Показатель | Дата | Уровень | Δ | z | Флаги |
|---|---|---|---|---|---|
| UMich Sentiment | 2026-10 | 76.4 | 0.0 | +1.4 |  |
| UMich Inflation Exp 1Y | 2026-10 | 4.1% | 0.0 | — |  |

UMich: FRED публикует с задержкой ~1 мес.

### 2.3 Рынок труда
| Показатель | Дата | Уровень | Изменение | YoY | 3mA / 3m ср. | z | Флаги |
|---|---|---|---|---|---|---|---|
| Unemployment Rate | 2026-09 | 5.4% | 0.0 | — | — | — |  |
| Nonfarm Payrolls | 2026-09 | — | +323 тыс. | — | 3m ср.: +157 тыс. | — |  |
| Initial Claims | 2026-10-17 | 268 242 | −2 367 | — | 4w ср.: 267 933 | +1.8 | rev |
| Continued Claims | 2026-10-10 | 2 238 135 | −4 942 | — | 4w ср.: 2 246 075 | +0.1 |  |
| Job Openings | 2026-08 | 9 353 тыс. | −7 тыс. | 2.9% | — | — |  |
| Job Openings Rate | 2026-08 | 5.6% | 0.0 | — | — | — |  |
| U/V | 2026-08 | 0.98 | 0.00 | — | — | — |  |
| Quits Rate | 2026-08 | 2.5% | 0.0 | — | — | — |  |
| Hires | 2026-08 | 6 804 тыс. | +27 тыс. | 2.2% | — | — |  |
| Avg Hourly Earnings | 2026-09 | — | — | 1.5% | −1.1% | — |  |
| Employment Cost Index | 2026-Q2 | — | QoQa 2.2% | 2.6% | — | — |  |
| Unit Labor Costs | 2026-Q2 | — | — | 2.2% | — | — |  |
| Labour Productivity | 2026-Q2 | — | — | 1.3% | — | — |  |

### 2.4 Инфляция
| Показатель | Дата | YoY | MoM | 3mA | 6mA | Флаги |
|---|---|---|---|---|---|---|
| CPI | 2026-09 | 1.7% | −0.1% | −1.1% | −0.5% |  |
| Core CPI | 2026-09 | 3.4% | 0.1% | 2.6% | 3.7% |  |
| Shelter CPI | 2026-09 | 1.0% | — | 4.2% | — |  |
| Supercore CPI (услуги без жилья) | 2026-09 | 1.6% | −0.1% | −1.1% | −0.6% |  |
| Sticky CPI | 2026-09 | 4.1% | — | — | — |  |
| PPI Final Demand | 2026-09 | 2.8% | 0.3% | — | — |  |
| Core PPI | 2026-09 | 3.1% | 0.3% | — | — |  |
| Core PPI ex trade | 2026-09 | 2.9% | −0.1% | — | — |  |
| PCE Price Index | 2026-08 | 0.6% | — | 0.5% | — |  |
| Core PCE | 2026-08 | 0.6% | 0.3% | 2.9% | 1.4% |  |

### 2.5 Инфляционные ожидания
| Показатель | Дата | Уровень | Δ1w (б.п.) | Δ4w (б.п.) |
|---|---|---|---|---|
| 5Y Breakeven | 2026-10-23 | 2.47% | −3 | −1 |
| 5Y5Y Forward | 2026-10-23 | 2.39% | +2 | 0 |
| 10Y Breakeven | 2026-10-23 | 2.43% | −1 | −1 |

### 2.6 ФРС и ожидания рынка
| Показатель | Дата | Уровень | Δ1w (б.п.) | Δ4w (б.п.) |
|---|---|---|---|---|
| Fed Funds | 2026-10-23 | 3.99% | −3 | −1 |
| UST 2Y | 2026-10-23 | 3.58% | −3 | 0 |
| M2 | 2026-09 | YoY 3.3% · 3mA 1.0% | — | — |

**Траектория ставки из фьючерсов (ZQ)**

Строки — месяц после каждого заседания FOMC в пределах 12 месяцев; implied rate = 100 − цена контракта. «Δ за заседание» — implied rate минус implied предыдущей строки (первой — минус DFF); P = |Δ| / 25 б.п., от 0 до 100%. Все строки — на одну дату цены; «неделю назад» — на 7 дней раньше. Флаги: `нет сделок` — нулевой объём, цена может быть неактуальной; `мало сделок` — средний объём за 5 дней меньше четверти, чем у предыдущей строки; `новый в окне` — контракт вошёл в 12 месяцев на этой неделе. Строки с флагами в итог не входят: итог и его изменение за неделю — по одному и тому же контракту.

| Контракт | После заседания | Implied rate | vs DFF (б.п.) | Δ за заседание (б.п.) | P(повышение/снижение) | Неделю назад | Флаги |
|---|---|---|---|---|---|---|---|
| Ноя 2026 | 2026-10-28 | 3.91% | −8 | −8 | снижение 32% | 3.92% |  |
| Янв 2027 | 2026-12-09 | 4.10% | +11 | +19 | повышение 76% | 4.15% |  |
| Фев 2027 | 2027-01-27 | 4.17% | +18 | +8 | повышение 32% | 4.23% |  |
| Апр 2027 | 2027-03-17 | 4.33% | +34 | +15 | повышение 60% | 4.41% |  |
| Май 2027 | 2027-04-28 | 4.40% | +41 | +8 | повышение 32% | 4.48% |  |
| Июл 2027 | 2027-06-09 | 4.50% | +51 | +10 | повышение 40% | 4.58% |  |
| Авг 2027 | 2027-07-28 | 4.53% | +54 | +3 | повышение 12% | 4.59% |  |
| Окт 2027 | 2027-09-15 | 4.56% | +57 | +3 | повышение 12% | 4.62% |  |

Цены на 2026-10-23.

Итог: к окт. 2027 заложено **~2.3 повышения** по 25 б.п. (неделю назад: ~2.4 повышения).

Ближайшие 3 заседания (до 2027-01-27): накоплено **+18 б.п.** (неделю назад: +22 б.п.).

### 2.7 Энергия
| Показатель | Дата | Уровень | Δ1w | Δ4w | YoY |
|---|---|---|---|---|---|
| WTI | 2026-10-23 | 103.27 $ | +1.8% | +11.8% | — |
| Henry Hub | 2026-10-23 | 3.08 $ | +1.0% | 0.0% | — |

## 3. Производные
| Показатель | Значение | Δ | z |
|---|---|---|---|
| U/V | 0.98 | 0.00 | — |
| Траектория ставки до окт. 2027 | +57 б.п. | −4 б.п. за неделю (меньше повышений) | — |

## 4. Официальные тексты ФРС

### 2026-10-21 — Речь — Governor Christopher J. Waller — «Economic Outlook»
Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. Inflation has moved down but remains above our 2 percent goal. […]
Ссылка: https://www.federalreserve.gov/newsevents/speech/waller20261021a.htm

### 2026-10-22 — Beige Book
Overall Economic Activity

Economic activity was little changed in most Districts.
Ссылка: https://www.federalreserve.gov/monetarypolicy/beigebook202611-summary.htm

### FOMC
Заседаний за неделю не было. Следующее: 2026-10-27/28 (решение 2026-10-28 14:00 ET, см. calendar_usa.md у мастера).

## 5. Пропущенные / устаревшие данные
- Устаревших рядов нет.
- Источник yahoo:DX-Y.NYB: ошибка с 2026-10-24 22:30 — yahoo DX-Y.NYB: GET …: status 429.

---
<!-- previous_report.md лежит рядом в папке проекта, если отчёт прошлой недели был сохранён -->
