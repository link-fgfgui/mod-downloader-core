package providers

import (
	"testing"

	modrinth "codeberg.org/jmansfield/go-modrinth/modrinth"
	cfSchema "github.com/sjet47/go-curseforge/schema"
)

func TestCurseForgeLogoURL(t *testing.T) {
	tests := []struct {
		name string
		logo cfSchema.ModAsset
		want string
	}{
		{
			name: "uses thumbnail url first",
			logo: cfSchema.ModAsset{
				ThumbnailUrl: " https://example.com/thumb.png ",
				URL:          "https://example.com/original.webp",
			},
			want: "https://example.com/thumb.png",
		},
		{
			name: "falls back to url",
			logo: cfSchema.ModAsset{
				URL: " https://media.forgecdn.net/avatars/1025/127/638548475358792693.webp ",
			},
			want: "https://media.forgecdn.net/avatars/1025/127/638548475358792693.webp",
		},
		{
			name: "returns empty when both are empty",
			logo: cfSchema.ModAsset{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := curseForgeLogoURL(tt.logo)
			if got != tt.want {
				t.Fatalf("curseForgeLogoURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCurseForgeModToModProjectCategories(t *testing.T) {
	project := curseForgeProvider{}.modToModProject(cfSchema.Mod{
		Name: "Example",
		Categories: []cfSchema.Category{
			{Slug: "technology", Name: "Technology"},
			{Name: "Magic"},
			{Slug: "technology"},
		},
	})
	want := []string{"technology", "magic"}
	assertStrings(t, project.Categories, want)
}

func TestModrinthSearchHitToModProjectCategories(t *testing.T) {
	title := "Example"
	projectID := "abc123"
	hit := &modrinth.SearchResult{
		Title:      &title,
		ProjectID:  &projectID,
		Categories: []string{"library", "Magic", "library"},
	}
	project := modrinthProvider{}.searchHitToModProject(hit)
	assertStrings(t, project.Categories, []string{"library", "magic"})
}

func TestModrinthProjectToModProjectCategories(t *testing.T) {
	title := "Example"
	projectID := "abc123"
	project := modrinthProvider{}.projectToModProject(&modrinth.Project{
		Title:                &title,
		ID:                   &projectID,
		Categories:           []string{"library", "technology"},
		AdditionalCategories: []string{"Magic", "library"},
	})
	assertStrings(t, project.Categories, []string{"library", "technology", "magic"})
}

func assertStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("strings[%d] = %q, want %q (all: %#v)", i, got[i], want[i], got)
		}
	}
}
