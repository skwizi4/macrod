---
region: usa
agent: master
file: markets
iso_week: 2026-W43
period: 2026-10-18 16:00 → 2026-10-25 16:00 (Europe/Bratislava)
generated_at: 2026-11-08T16:00:04+01:00
kind: weekly
sources_ok: []
sources_failed: [yahoo, fred]
stale_series: []
---

# USA Markets — 2026-W43

## 1. Рынки

Формат: цены закрытия на дату; Δ1w / Δ4w — в % для цен и индексов, в пунктах для VIX и MOVE, в б.п. для спредов; `от MA50` / `от MA200` — расстояние цены до 50/200-дневной средней, %; `z` — z-score за 52 нед. Даты: последний торговый день, Bitcoin — последний календарный; HY/IG (FRED) — на день раньше, FRED публикует их со сдвигом. Флаги: `FRED fallback` — Yahoo недоступен, взят запасной ряд FRED; `нет close MM-DD` — закрытия этого дня ещё нет.

| Показатель | Дата | Уровень | Δ1w | Δ4w | от MA50 | от MA200 | z | Флаги |
|---|---|---|---|---|---|---|---|---|
| NASDAQ Composite (запас) | 2026-10-23 | 23 602 | +0.3% | +0.4% | −0.3% | 0.0% | — | FRED fallback |
| NASDAQ fut | 2026-10-23 | 29 602 | −0.6% | — | — | — | — |  |
| S&P 500 | 2026-10-23 | 7 669 | −1.4% | −0.1% | — | — | — |  |
| VIX | 2026-10-23 | 19.45 | −0.54 | +4.58 | — | — | +0.4 |  |
| MOVE | 2026-10-23 | 102.27 | −0.37 | −0.20 | — | — | +1.3 |  |
| HY Spread | 2026-10-23 | 3.18% | −4 б.п. | −1 б.п. | — | — | +0.1 |  |
| IG Spread | 2026-10-23 | 0.83% | +1 б.п. | 0 б.п. | — | — | — |  |
| Dollar Broad (запас) | 2026-10-23 | 124.74 | +1.5% | +0.4% | — | — | — | FRED fallback |
| Gold | 2026-10-23 | 4 055.68 $ | +0.5% | +0.4% | — | — | — |  |
| Copper | 2026-10-23 | 6.56 $ | −0.8% | +0.2% | — | — | — |  |
| Bitcoin | 2026-10-23 | 81 074 $ | −0.5% | −3.5% | — | — | — |  |

## 2. Пропущенные / устаревшие данные
- Устаревших рядов нет.
- Источник fred:DGS10: ошибка с 2026-10-25 06:15 — fred:DGS10: status 503.
- Источник fred:WALCL: ошибка с 2026-10-25 06:15 — fred:WALCL: status 503.
- Источник yahoo:BTC-USD: ошибка с 2026-10-25 06:15 — yahoo:BTC-USD: status 503.
- Источник yahoo:BZ=F: ошибка с 2026-10-25 06:15 — yahoo:BZ=F: status 503.
- Источник yahoo:CL=F: ошибка с 2026-10-25 06:15 — yahoo:CL=F: status 503.
- Источник yahoo:CNY=X: ошибка с 2026-10-25 06:15 — yahoo:CNY=X: status 503.
- Источник yahoo:DX-Y.NYB: ошибка с 2026-10-25 06:15 — yahoo:DX-Y.NYB: status 503.
- Источник yahoo:EURUSD=X: ошибка с 2026-10-25 06:15 — yahoo:EURUSD=X: status 503.
- Источник yahoo:GC=F: ошибка с 2026-10-25 06:15 — yahoo:GC=F: status 503.
- Источник yahoo:HG=F: ошибка с 2026-10-25 06:15 — yahoo:HG=F: status 503.
- Источник yahoo:IWM: ошибка с 2026-10-25 06:15 — yahoo:IWM: status 503.
- Источник yahoo:NG=F: ошибка с 2026-10-25 06:15 — yahoo:NG=F: status 503.
- Источник yahoo:NQ=F: ошибка с 2026-10-25 06:15 — yahoo:NQ=F: status 503.
- Источник yahoo:QQEW: ошибка с 2026-10-25 06:15 — yahoo:QQEW: status 503.
- Источник yahoo:QQQ: ошибка с 2026-10-25 06:15 — yahoo:QQQ: status 503.
- Источник yahoo:RSP: ошибка с 2026-10-25 06:15 — yahoo:RSP: status 503.
- Источник yahoo:SMH: ошибка с 2026-10-25 06:15 — yahoo:SMH: status 503.
- Источник yahoo:SPY: ошибка с 2026-10-25 06:15 — yahoo:SPY: status 503.
- Источник yahoo:TTF=F: ошибка с 2026-10-25 06:15 — yahoo:TTF=F: status 503.
- Источник yahoo:XLE: ошибка с 2026-10-25 06:15 — yahoo:XLE: status 503.
- Источник yahoo:XLP: ошибка с 2026-10-25 06:15 — yahoo:XLP: status 503.
- Источник yahoo:XLU: ошибка с 2026-10-25 06:15 — yahoo:XLU: status 503.
- Источник yahoo:ZT=F: ошибка с 2026-10-25 06:15 — yahoo:ZT=F: status 503.
- Источник yahoo:^GDAXI: ошибка с 2026-10-25 06:15 — yahoo:^GDAXI: status 503.
- Источник yahoo:^GSPC: ошибка с 2026-10-25 06:15 — yahoo:^GSPC: status 503.
- Источник yahoo:^MOVE: ошибка с 2026-10-25 06:15 — yahoo:^MOVE: status 503.
- Источник yahoo:^NDX: ошибка с 2026-10-25 06:15 — yahoo:^NDX: status 503.
- Источник yahoo:^SKEW: ошибка с 2026-10-25 06:15 — yahoo:^SKEW: status 503.
- Источник yahoo:^STOXX50E: ошибка с 2026-10-25 06:15 — yahoo:^STOXX50E: status 503.
- Источник yahoo:^VIX3M: ошибка с 2026-10-25 06:15 — yahoo:^VIX3M: status 503.
- Источник yahoo:^VIX: ошибка с 2026-10-25 06:15 — yahoo:^VIX: status 503.
