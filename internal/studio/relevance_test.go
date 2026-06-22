package studio

import (
	"strconv"
	"testing"
)

func TestFilterCatalog_SmallIsUnchanged(t *testing.T) {
	cat := Catalog{
		Skills:         []CatalogSkill{{Name: "yfinance"}, {Name: "weather"}},
		KnowledgeBases: []CatalogKB{{Name: "kb1"}},
	}
	out := FilterCatalogForIntent("anything", cat)
	if len(out.Skills) != 2 || len(out.KnowledgeBases) != 1 {
		t.Errorf("small catalog should be unchanged, got %d skills / %d kbs", len(out.Skills), len(out.KnowledgeBases))
	}
}

func TestFilterCatalog_TrimsAndKeepsRelevant(t *testing.T) {
	var skills []CatalogSkill
	// One clearly-relevant skill plus many noise skills over the cap.
	skills = append(skills, CatalogSkill{Name: "stocks", Description: "stock market quotes and finance data"})
	for i := 0; i < maxGroundedSkills+5; i++ {
		skills = append(skills, CatalogSkill{Name: "noise" + strconv.Itoa(i), Description: "unrelated capability"})
	}
	cat := Catalog{Skills: skills}
	out := FilterCatalogForIntent("get me stock market finance quotes", cat)
	if len(out.Skills) != maxGroundedSkills {
		t.Fatalf("expected trim to %d, got %d", maxGroundedSkills, len(out.Skills))
	}
	found := false
	for _, s := range out.Skills {
		if s.Name == "stocks" {
			found = true
		}
	}
	if !found {
		t.Error("relevant skill 'stocks' was dropped during trim")
	}
}

func TestFilterCatalog_KeepsNamedSkill(t *testing.T) {
	var skills []CatalogSkill
	for i := 0; i < maxGroundedSkills+10; i++ {
		skills = append(skills, CatalogSkill{Name: "noise" + strconv.Itoa(i)})
	}
	// A named skill with no descriptive overlap must still be kept.
	skills = append(skills, CatalogSkill{Name: "zzqux"})
	cat := Catalog{Skills: skills}
	out := FilterCatalogForIntent("please use the zzqux skill", cat)
	found := false
	for _, s := range out.Skills {
		if s.Name == "zzqux" {
			found = true
		}
	}
	if !found {
		t.Error("explicitly named skill 'zzqux' should always be kept")
	}
}

func TestFilterCatalog_TrimsMCPToolsKeepsNamedServer(t *testing.T) {
	var tools []CatalogMCPTool
	for i := 0; i < maxGroundedMCPTools+10; i++ {
		tools = append(tools, CatalogMCPTool{Name: "mcp__big__t" + strconv.Itoa(i), Description: "noise"})
	}
	cat := Catalog{MCP: []CatalogMCPServer{
		{Server: "notebooklm", Tools: []CatalogMCPTool{{Name: "mcp__notebooklm__create", Description: "make a notebook"}}},
		{Server: "big", Tools: tools},
	}}
	out := FilterCatalogForIntent("create a notebooklm notebook", cat)
	// notebooklm (named) must survive with its tool.
	var nb *CatalogMCPServer
	for i := range out.MCP {
		if out.MCP[i].Server == "notebooklm" {
			nb = &out.MCP[i]
		}
	}
	if nb == nil || len(nb.Tools) == 0 {
		t.Fatalf("named server notebooklm should be kept: %+v", out.MCP)
	}
	if countMCPTools(out.MCP) > maxGroundedMCPTools {
		t.Errorf("MCP tools not capped: %d", countMCPTools(out.MCP))
	}
}
