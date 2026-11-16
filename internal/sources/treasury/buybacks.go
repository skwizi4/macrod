package treasury

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"macro-collector/internal/model"
)

const endpointBuybacks = "v1/accounting/od/buybacks_operations"

// FetchBuybacks returns buyback operations from since on.
func (c *Client) FetchBuybacks(ctx context.Context, since time.Time) ([]model.Buyback, error) {
	rows, err := c.fiscal(ctx, endpointBuybacks, url.Values{
		"fields": {"operation_date,operation_start_time_est,operation_type,security_type,maturity_bucket,total_par_amt_offered,total_par_amt_accepted,max_par_amt_redeemed"},
		"filter": {"operation_date:gte:" + since.Format(time.DateOnly)},
		"sort":   {"operation_date"},
	})
	if err != nil {
		return nil, fmt.Errorf("treasury buybacks: %w", err)
	}
	out := make([]model.Buyback, 0, len(rows))
	for _, r := range rows {
		b, err := parseBuyback(r)
		if err != nil {
			return nil, fmt.Errorf("treasury buybacks %s: %w", r["operation_date"], err)
		}
		out = append(out, b)
	}
	return out, nil
}

func parseBuyback(r row) (model.Buyback, error) {
	b := model.Buyback{
		OperationType:  null(r["operation_type"]),
		SecurityType:   null(r["security_type"]),
		MaturityBucket: null(r["maturity_bucket"]),
	}
	var err error
	if b.OperationDate, err = date(r["operation_date"]); err != nil {
		return b, err
	}
	if b.StartTime, err = clock(r["operation_start_time_est"]); err != nil {
		return b, err
	}
	for f, dst := range map[string]*model.Num{
		"total_par_amt_offered":  &b.ParOffered,
		"total_par_amt_accepted": &b.ParAccepted,
		"max_par_amt_redeemed":   &b.MaxRedeemed,
	} {
		v, ok, err := number(r[f])
		if err != nil {
			return b, fmt.Errorf("%s: %w", f, err)
		}
		if ok {
			*dst = model.Some(v)
		}
	}
	return b, nil
}

func null(s string) string {
	if s == "null" {
		return ""
	}
	return s
}
