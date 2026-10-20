package main

type Repo struct {
	Rank        int    `json:"rank"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Language    string `json:"language"`
	StarsToday  int    `json:"starsToday"`
	StarsTotal  string `json:"starsTotal"`
	Readme      string `json:"readme,omitempty"`
}

type LeadCopy struct {
	Headline string   `json:"headline"`
	Dek      string   `json:"dek"`
	Body     []string `json:"body"`
}

type BriefCopy struct {
	Repo     string `json:"repo"`
	Headline string `json:"headline"`
	Body     string `json:"body"`
}

type TrendCopy struct {
	Title string   `json:"title"`
	Body  []string `json:"body"`
}

type Copy struct {
	Lead       LeadCopy    `json:"lead"`
	Briefs     []BriefCopy `json:"briefs"`
	Trend      TrendCopy   `json:"trend"`
	EditorNote string      `json:"editor_note"`
}

type Fixture struct {
	Date    string `json:"date"`
	IssueNo int    `json:"issueNo"`
	Repos   []Repo `json:"repos"`
	Copy    Copy   `json:"copy"`
}

type BriefView struct {
	Repo     Repo
	Headline string
	Body     string
}

type LangStat struct {
	Language string
	Count    int
	Percent  int
}

type IssueView struct {
	Date        string
	DateCN      string
	IssueNo     int
	Count       int
	Lead        Repo
	LeadBody    []string
	Briefs      []BriefView
	TrendTitle  string
	TrendBody   []string
	EditorNote  string
	TotalStars  int
	LangStats   []LangStat
	TopThree    []Repo
	SiteURL     string
	WebURL      string
	Subject     string
	Preheader   string
}
