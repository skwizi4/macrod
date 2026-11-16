package fred

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Maximum page size of fred/releases/dates.
const maxReleaseDatesLimit = 1000

// Release is a FRED release (a statistical publication such as CPI or H.15).
type Release struct {
	ID   int
	Name string
}

type seriesReleaseResponse struct {
	Releases []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"releases"`
}

// FetchSeriesRelease returns the release that publishes series id
// (fred/series/release).
func (c *Client) FetchSeriesRelease(ctx context.Context, id string) (Release, error) {
	var resp seriesReleaseResponse
	if err := c.get(ctx, "series/release", url.Values{"series_id": {id}}, &resp); err != nil {
		return Release{}, fmt.Errorf("fred series release %s: %w", id, err)
	}
	if len(resp.Releases) != 1 {
		return Release{}, fmt.Errorf("fred series release %s: got %d releases", id, len(resp.Releases))
	}
	r := resp.Releases[0]
	return Release{ID: r.ID, Name: r.Name}, nil
}

// ReleaseDate is one scheduled or past publication date of a release.
type ReleaseDate struct {
	ReleaseID   int
	ReleaseName string
	Date        time.Time // calendar day, UTC midnight
}

type releasesDatesResponse struct {
	Count        int `json:"count"`
	ReleaseDates []struct {
		ReleaseID   int    `json:"release_id"`
		ReleaseName string `json:"release_name"`
		Date        string `json:"date"`
	} `json:"release_dates"`
}

// FetchReleasesDates returns the dates of all releases from from to to
// (calendar days, both inclusive), including future dates
// (fred/releases/dates with include_release_dates_with_no_data).
func (c *Client) FetchReleasesDates(ctx context.Context, from, to time.Time) ([]ReleaseDate, error) {
	var out []ReleaseDate
	for offset := 0; ; {
		params := url.Values{
			"realtime_start":                     {from.Format(time.DateOnly)},
			"realtime_end":                       {to.Format(time.DateOnly)},
			"include_release_dates_with_no_data": {"true"},
			"order_by":                           {"release_date"},
			"sort_order":                         {"asc"},
			"limit":                              {strconv.Itoa(min(c.pageLimit, maxReleaseDatesLimit))},
			"offset":                             {strconv.Itoa(offset)},
		}
		var resp releasesDatesResponse
		if err := c.get(ctx, "releases/dates", params, &resp); err != nil {
			return nil, fmt.Errorf("fred releases dates: %w", err)
		}
		for _, d := range resp.ReleaseDates {
			date, err := time.Parse(time.DateOnly, d.Date)
			if err != nil {
				return nil, fmt.Errorf("fred releases dates %d date %q: %w", d.ReleaseID, d.Date, err)
			}
			out = append(out, ReleaseDate{ReleaseID: d.ReleaseID, ReleaseName: d.ReleaseName, Date: date})
		}
		offset += len(resp.ReleaseDates)
		if len(resp.ReleaseDates) == 0 || offset >= resp.Count {
			return out, nil
		}
	}
}
