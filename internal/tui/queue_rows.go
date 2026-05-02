package tui

type issueKey struct {
	projectID int64
	number    int64
}

type queueRow struct {
	issue       Issue
	key         issueKey
	depth       int
	hasChildren bool
	expanded    bool
	context     bool
	lastChild   bool
}

type expansionSet map[issueKey]bool

func buildQueueRows(issues []Issue, filter ListFilter, expanded expansionSet) []queueRow {
	state := newQueueBuildState(issues, filter, expanded)
	for _, key := range state.order {
		iss := state.byKey[key]
		if iss.ParentNumber != nil && state.hasIssue(issueKey{projectID: iss.ProjectID, number: *iss.ParentNumber}) {
			continue
		}
		state.appendNode(key, 0, false, nil)
	}
	for _, key := range state.order {
		if state.emitted[key] || !state.included[key] {
			continue
		}
		state.appendNode(key, 0, false, nil)
	}
	return state.rows
}

type queueBuildState struct {
	byKey            map[issueKey]Issue
	childrenByParent map[issueKey][]issueKey
	order            []issueKey
	filter           ListFilter
	filterActive     bool
	expanded         expansionSet
	matched          map[issueKey]bool
	included         map[issueKey]bool
	emitted          map[issueKey]bool
	rows             []queueRow
}

func newQueueBuildState(issues []Issue, filter ListFilter, expanded expansionSet) *queueBuildState {
	state := &queueBuildState{
		byKey:            make(map[issueKey]Issue, len(issues)),
		childrenByParent: make(map[issueKey][]issueKey),
		order:            make([]issueKey, 0, len(issues)),
		filter:           filter,
		filterActive:     hasActiveQueueFilter(filter),
		expanded:         expanded,
		matched:          make(map[issueKey]bool, len(issues)),
		included:         make(map[issueKey]bool, len(issues)),
		emitted:          make(map[issueKey]bool, len(issues)),
	}
	for _, iss := range issues {
		key := issueKey{projectID: iss.ProjectID, number: iss.Number}
		state.byKey[key] = iss
		state.order = append(state.order, key)
	}
	for _, key := range state.order {
		iss := state.byKey[key]
		if iss.ParentNumber == nil {
			continue
		}
		parentKey := issueKey{projectID: iss.ProjectID, number: *iss.ParentNumber}
		if state.hasIssue(parentKey) {
			state.childrenByParent[parentKey] = append(state.childrenByParent[parentKey], key)
		}
	}
	state.computeIncluded()
	return state
}

func (s *queueBuildState) computeIncluded() {
	if !s.filterActive {
		return
	}
	for _, key := range s.order {
		iss := s.byKey[key]
		if !matchesFilter(iss, s.filter) {
			continue
		}
		s.matched[key] = true
		s.included[key] = true
		s.includeAncestors(key)
	}
}

func (s *queueBuildState) includeAncestors(key issueKey) {
	seen := map[issueKey]bool{key: true}
	for {
		iss := s.byKey[key]
		if iss.ParentNumber == nil {
			return
		}
		parentKey := issueKey{projectID: iss.ProjectID, number: *iss.ParentNumber}
		if seen[parentKey] || !s.hasIssue(parentKey) {
			return
		}
		s.included[parentKey] = true
		seen[parentKey] = true
		key = parentKey
	}
}

func (s *queueBuildState) appendNode(key issueKey, depth int, lastChild bool, seenPath map[issueKey]bool) {
	if s.filterActive && !s.included[key] {
		return
	}
	if seenPath == nil {
		seenPath = map[issueKey]bool{}
	}
	if seenPath[key] {
		return
	}
	seenPath[key] = true
	iss := s.byKey[key]
	hasChildren := len(s.childrenByParent[key]) > 0
	isExpanded := s.expanded != nil && s.expanded[key]
	if s.filterActive && len(s.visibleChildKeys(key, true)) > 0 {
		isExpanded = true
	}
	s.rows = append(s.rows, queueRow{
		issue:       iss,
		key:         key,
		depth:       depth,
		hasChildren: hasChildren,
		expanded:    isExpanded,
		context:     s.filterActive && s.included[key] && !s.matched[key],
		lastChild:   lastChild,
	})
	s.emitted[key] = true
	childKeys := s.visibleChildKeys(key, isExpanded)
	for i, childKey := range childKeys {
		nextSeen := make(map[issueKey]bool, len(seenPath)+1)
		for seenKey, seen := range seenPath {
			nextSeen[seenKey] = seen
		}
		s.appendNode(childKey, depth+1, i == len(childKeys)-1, nextSeen)
	}
}

func (s *queueBuildState) visibleChildKeys(parent issueKey, expanded bool) []issueKey {
	children := s.childrenByParent[parent]
	if len(children) == 0 {
		return nil
	}
	if !s.filterActive {
		if !expanded {
			return nil
		}
		return children
	}
	out := make([]issueKey, 0, len(children))
	for _, child := range children {
		if s.included[child] {
			out = append(out, child)
		}
	}
	return out
}

func (s *queueBuildState) hasIssue(key issueKey) bool {
	_, ok := s.byKey[key]
	return ok
}

func hasActiveQueueFilter(f ListFilter) bool {
	return f.Status != "" || f.Owner != "" || f.Author != "" || f.Search != "" || len(f.Labels) > 0
}
