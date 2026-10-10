// Package progress builds AndRho's public development progress: the commits
// of every AndRho repository on GitHub merged into one timeline, with the
// combined contributor list and totals. andrho's landing shows it so the
// "still in development" claim can be checked commit by commit.
//
// GitHub's unauthenticated API allows 60 requests/hour per IP. Fetching from
// here (optionally with GITHUB_TOKEN, 5000/hour) and caching the result in
// Redis keeps every visitor's browser off that limit.
package progress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Repo is one repository's slice of the progress.
type Repo struct {
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	Description  string    `json:"description"`
	Commits      int       `json:"commits"`
	CreatedAt    time.Time `json:"createdAt"`
	LastCommitAt time.Time `json:"lastCommitAt"`
}

// Contributor is a person summed across every repo.
type Contributor struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatarUrl"`
	URL       string `json:"url"`
	Commits   int    `json:"commits"`
}

// Commit is one entry of the merged timeline.
type Commit struct {
	Repo    string    `json:"repo"`
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	URL     string    `json:"url"`
	Date    time.Time `json:"date"`
	Author  string    `json:"author"`
	Avatar  string    `json:"avatarUrl"`
}

// Progress is the payload of GET /progress.
type Progress struct {
	Repos          []Repo        `json:"repos"`
	TotalCommits   int           `json:"totalCommits"`
	CommitsPerWeek float64       `json:"commitsPerWeek"`
	Since          time.Time     `json:"since"`
	Contributors   []Contributor `json:"contributors"`
	Recent         []Commit      `json:"recent"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

// Recent commits taken per repo before merging the timeline.
const perRepoRecent = 12

// Client fetches from the GitHub REST API.
type Client struct {
	HTTP  *http.Client
	Token string // optional
	Base  string // https://api.github.com, overridable in tests
}

func NewClient(token string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 12 * time.Second}, Token: token, Base: "https://api.github.com"}
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "andrho-api")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github %s: %s", path, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

type ghRepo struct {
	FullName    string    `json:"full_name"`
	HTMLURL     string    `json:"html_url"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type ghContributor struct {
	Login         string `json:"login"`
	AvatarURL     string `json:"avatar_url"`
	HTMLURL       string `json:"html_url"`
	Contributions int    `json:"contributions"`
}

type ghCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"author"`
}

type repoData struct {
	repo         ghRepo
	contributors []ghContributor
	commits      []ghCommit
	err          error
}

func (c *Client) fetchRepo(ctx context.Context, fullName string) repoData {
	var d repoData
	if d.err = c.get(ctx, "/repos/"+fullName, &d.repo); d.err != nil {
		return d
	}
	if d.err = c.get(ctx, "/repos/"+fullName+"/contributors?per_page=100", &d.contributors); d.err != nil {
		return d
	}
	d.err = c.get(ctx, fmt.Sprintf("/repos/%s/commits?per_page=%d", fullName, perRepoRecent), &d.commits)
	return d
}

// Subject keeps the first line of a commit message without its
// conventional-commit prefix: "feat(waitlist): add X" → "add X".
func Subject(message string) string {
	line := strings.SplitN(message, "\n", 2)[0]
	if i := strings.Index(line, "):"); i != -1 && i < 60 {
		return strings.TrimSpace(line[i+2:])
	}
	if i := strings.Index(line, ": "); i != -1 && i < 20 {
		return strings.TrimSpace(line[i+2:])
	}
	return strings.TrimSpace(line)
}

// Fetch builds the merged progress for `repos` ("owner/name"). Repos that
// fail are skipped; it errors only when none could be read.
func (c *Client) Fetch(ctx context.Context, repos []string, recent int) (*Progress, error) {
	results := make([]repoData, len(repos))
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Add(1)
		go func(i int, r string) {
			defer wg.Done()
			results[i] = c.fetchRepo(ctx, r)
		}(i, r)
	}
	wg.Wait()

	p := &Progress{Repos: []Repo{}, Contributors: []Contributor{}, Recent: []Commit{}, UpdatedAt: time.Now().UTC()}
	people := map[string]*Contributor{}
	var lastErr error
	for _, d := range results {
		if d.err != nil {
			lastErr = d.err
			continue
		}
		name := d.repo.FullName[strings.Index(d.repo.FullName, "/")+1:]
		repo := Repo{Name: name, URL: d.repo.HTMLURL, Description: d.repo.Description, CreatedAt: d.repo.CreatedAt}
		for _, ct := range d.contributors {
			repo.Commits += ct.Contributions
			if people[ct.Login] == nil {
				people[ct.Login] = &Contributor{Login: ct.Login, AvatarURL: ct.AvatarURL, URL: ct.HTMLURL}
			}
			people[ct.Login].Commits += ct.Contributions
		}
		for _, cm := range d.commits {
			entry := Commit{Repo: name, SHA: cm.SHA[:min(7, len(cm.SHA))], Subject: Subject(cm.Commit.Message), URL: cm.HTMLURL, Date: cm.Commit.Author.Date, Author: cm.Commit.Author.Name}
			if cm.Author != nil {
				entry.Author, entry.Avatar = cm.Author.Login, cm.Author.AvatarURL
			}
			if entry.Date.After(repo.LastCommitAt) {
				repo.LastCommitAt = entry.Date
			}
			p.Recent = append(p.Recent, entry)
		}
		p.TotalCommits += repo.Commits
		if p.Since.IsZero() || repo.CreatedAt.Before(p.Since) {
			p.Since = repo.CreatedAt
		}
		p.Repos = append(p.Repos, repo)
	}
	if len(p.Repos) == 0 {
		return nil, fmt.Errorf("progress: no repository could be read: %w", lastErr)
	}

	for _, ct := range people {
		p.Contributors = append(p.Contributors, *ct)
	}
	sort.Slice(p.Contributors, func(i, j int) bool { return p.Contributors[i].Commits > p.Contributors[j].Commits })
	sort.Slice(p.Recent, func(i, j int) bool { return p.Recent[i].Date.After(p.Recent[j].Date) })
	if len(p.Recent) > recent {
		p.Recent = p.Recent[:recent]
	}
	sort.Slice(p.Repos, func(i, j int) bool { return p.Repos[i].LastCommitAt.After(p.Repos[j].LastCommitAt) })

	weeks := time.Since(p.Since).Hours() / (24 * 7)
	if weeks < 1 {
		weeks = 1
	}
	p.CommitsPerWeek = float64(int(float64(p.TotalCommits)/weeks*10)) / 10
	return p, nil
}
