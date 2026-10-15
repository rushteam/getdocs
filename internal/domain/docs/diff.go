package docs

import "slices"

// Diff 计算两个快照之间的文档变更；prev 为 nil 时全部视为新增。
func Diff(prev, cur *Snapshot) ChangeSet {
	var cs ChangeSet
	old := map[string]string{}
	if prev != nil {
		for _, d := range prev.Docs {
			old[d.Path] = d.Hash
		}
	}
	for _, d := range cur.Docs {
		h, ok := old[d.Path]
		switch {
		case !ok:
			cs.Added = append(cs.Added, d.Path)
		case h != d.Hash:
			cs.Modified = append(cs.Modified, d.Path)
		default:
			cs.Unchanged++
		}
		delete(old, d.Path)
	}
	for p := range old {
		cs.Deleted = append(cs.Deleted, p)
	}
	slices.Sort(cs.Added)
	slices.Sort(cs.Modified)
	slices.Sort(cs.Deleted)
	cs.AssetsChanged = prev == nil && len(cur.Assets) > 0 || prev != nil && !sameAssets(prev.Assets, cur.Assets)
	return cs
}

func sameAssets(a, b []Asset) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]string, len(a))
	for _, x := range a {
		m[x.Path] = x.Hash
	}
	for _, x := range b {
		if h, ok := m[x.Path]; !ok || h != x.Hash {
			return false
		}
	}
	return true
}
