(() => {
  const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const types = {thinking:'LLM request',chat:'Chat',compaction:'Compaction',search:'Searching',read:'Reading',edit:'Editing',test:'Testing',build:'Building',delegate:'Delegating',message:'Messaging',browser:'Browser',command:'Command'};
  const clock = seconds => seconds < 60 ? `${seconds.toFixed(1)}s` : seconds < 3600 ? `${(seconds / 60).toFixed(1)}m` : `${(seconds / 3600).toFixed(1)}h`;

  // The last compaction of each back-to-back run. compact_in_place index calls
  // and the LLM requests that issued compact_in_place are bookkeeping, as are
  // indirect calls, so they do not break a run.
  function lastCompactions(events) {
    const requests = new Set(events.filter(e => e.label === 'compact_in_place').map(e => e.request_id));
    const steps = events.filter(e => e.category === 'compaction' || !(e.label === 'compact_in_place' || e.purpose || requests.has(e.id)));
    return steps.filter((e, i) => e.category === 'compaction' && steps[i + 1]?.category !== 'compaction');
  }

  function inputDetail(e) {
    if (e.kind !== 'tool') return e.purpose ? `<p>Purpose: ${esc(e.purpose)}</p>` : '';
    let summary = '';
    if (e.category === 'compaction') {
      const ranges = e.input.collapse || [], latest = ranges.at(-1);
      summary = `<p class="compact-summary">${ranges.length} summary ranges · ${(e.input.trim || []).length} trim targets</p>`;
      if (latest) summary += `<details class="compact-note"><summary>Latest summary · messages ${esc(latest.from)}–${esc(latest.to)}</summary><p>${esc(latest.note)}</p></details>`;
    } else if (e.input?.command) {
      summary = `<pre class="tool-command">${esc(e.input.command)}</pre>`;
    }
    return `${summary}<details class="raw-input" data-index="${e.index}"><summary>Raw tool input</summary><pre></pre></details>`;
  }

  function historyRow(e, first) {
    const duration = (e.timed ? `Recorded duration: ${clock(e.seconds)}.` : 'Duration was not recorded; shown as a point.')
      + (e.kind === 'llm' ? ' Request time is not isolated thinking time.' : '')
      + (e.tool_seconds != null ? ` Includes the requesting LLM call${e.request_seconds != null ? ` (${clock(e.request_seconds)})` : ''} and tool execution (${clock(e.tool_seconds)}).` : '');
    return `<details class="activity-row" id="activity-${e.index}">
      <summary>
        <span class="activity-arrow"></span>
        <span class="activity-at">${clock((e.begin - first) / 1000)}</span>
        <span class="activity-kind"><i class="sw t-${e.category}"></i>${types[e.category]}</span>
        <span class="activity-label" title="${esc(e.actor)}">${esc(e.label)}${e.error ? ' <strong class="error">failed</strong>' : ''}</span>
        <span class="activity-duration">${e.timed ? clock(e.seconds) : 'Unknown'}</span>
      </summary>
      <div class="activity-detail">
        <p>${esc(e.actor)}${e.model ? ` · ${esc(e.model)}` : ''} · ${esc(new Date(e.begin).toLocaleString())}</p>
        <p>${duration}</p>
        ${e.kind === 'llm' ? `<p>Tokens: ${esc(e.tokens.toLocaleString())} · output ${esc(e.usage.output_tokens.toLocaleString())}</p>` : ''}
        ${inputDetail(e)}
      </div>
    </details>`;
  }

  function render(root, lanes, showIdle = false, contextId = lanes[0]?.id) {
    const graphsOpen = !!root.querySelector('[data-usage]')?.open;
    TimelineGraphs.unmount();
    const drawn = lanes.filter(lane => lane.events.length);
    const all = drawn.flatMap(lane => lane.events).sort((a, b) => a.begin - b.begin);
    if (!all.length) { root.innerHTML = '<p class="muted">No recorded activity for this conversation.</p>'; return; }
    all.forEach((e, i) => e.index = i);

    // Idle: gaps of 10s+ with nothing running on any agent.
    const first = all[0].begin, end = Math.max(...all.map(e => e.finish)), idle = [];
    let busyUntil = first;
    for (const e of all) {
      if (e.begin - busyUntil >= 10000) idle.push({start: busyUntil, end: e.begin});
      busyUntil = Math.max(busyUntil, e.finish);
    }
    const idleMs = idle.reduce((sum, gap) => sum + gap.end - gap.start, 0);
    const position = time => time - first - (showIdle ? 0 : idle.reduce((sum, gap) => sum + Math.max(0, Math.min(time, gap.end) - gap.start), 0));
    const span = Math.max(position(end), 1), x = time => position(time) / span, pct = f => `${f * 100}%`;

    let labels = '', tracks = '';
    drawn.forEach((lane, laneIndex) => {
      const slots = [], rows = [];
      for (const e of lane.events) {
        let slot = slots.findIndex(until => until <= e.begin);
        if (slot < 0) { slot = slots.length; slots.push(0); rows.push(''); }
        slots[slot] = e.finish;
        const width = x(e.finish) - x(e.begin);
        const title = esc(`${types[e.category]} · ${e.label} · ${e.timed ? clock(e.seconds) : 'duration unknown'}${e.error ? ' · failed' : ''}`);
        rows[slot] += `<button class="blk t-${e.category}${e.timed ? '' : ' point'}${e.error ? ' err' : ''}" data-index="${e.index}" data-kind="${e.category}" style="left:${pct(x(e.begin))};width:${pct(width)}" title="${title}" aria-label="${title}" aria-expanded="false">${width > .05 ? types[e.category] : ''}</button>`;
      }
      let until = lane.events[0].begin, gaps = '';
      for (const e of lane.events) {
        const gap = e.begin - until;
        if (showIdle && gap >= 10000) gaps += `<span class="gap" style="left:${pct((until - first) / span)};width:${pct(gap / span)}" title="No recorded activity · ${clock(gap / 1000)}">${gap / span > .04 ? clock(gap / 1000) : ''}</span>`;
        until = Math.max(until, e.finish);
      }
      rows.forEach((blocks, i) => {
        const name = i ? `Parallel ${i}` : laneIndex === 0 && lane.main ? 'main' : lane.name;
        labels += `<div class="lane-label" title="${esc(lane.name)}"><span>${esc(name)}</span>${i ? '' : `<small>${esc(lane.model || '')}</small>`}</div>`;
        tracks += `<div class="lane"><div class="track">${i ? '' : gaps}${blocks}</div></div>`;
      });
    });
    const compactions = drawn.flatMap(lane => lastCompactions(lane.events)).map(e => ({x: x(e.finish), title: `Compaction · ${e.actor}${e.error ? ' · failed' : ''}`}));
    const markers = compactions.map(c => `<span class="compaction-marker" style="left:${pct(c.x)}" title="${esc(c.title)}"></span>`).join('');
    const present = new Set(all.map(e => e.category));
    const legend = Object.entries(types).filter(([key]) => present.has(key)).map(([key, label]) => `<button class="legend-filter" data-kind-filter="${key}" aria-pressed="false"><i class="sw t-${key}"></i>${label}</button>`).join('');
    const axis = Array.from({length: 6}, (_, i) => `<span style="left:${i * 20}%">${clock(span / 1000 * i / 5)}</span>`).join('');
    const options = lanes.map(lane => `<option value="${esc(lane.id)}"${lane.id === contextId ? ' selected' : ''}>${esc(lane.name)}</option>`).join('');

    root.innerHTML = `<section class="tl">
      <div class="tl-heading">
        <span>${drawn.length} agent${drawn.length === 1 ? '' : 's'} · ${clock(span / 1000)} shown${!showIdle && idleMs ? ` · ${clock(idleMs / 1000)} idle hidden` : ''}</span>
        <div class="tl-controls">
          <label class="idle-toggle"><input type="checkbox" data-idle${showIdle ? ' checked' : ''}> Show idle time</label>
          <label>Zoom <select data-zoom><option value="1">Fit</option><option value="2">2×</option><option value="4">4×</option><option value="8">8×</option></select></label>
        </div>
      </div>
      <p class="timeline-help">Click a block for its history. Hover a category to focus; click to filter. ${showIdle ? 'Real elapsed time.' : 'Gaps of 10s+ with no recorded activity across any agent are removed; history keeps real timestamps.'} Dashed blocks have unknown duration.</p>
      <div class="legend">${legend}<button class="legend-filter legend-clear" disabled>Clear</button></div>
      <div class="timeline-grid">
        <div class="lane-labels">${labels}
          <div class="native-graph-label"><strong>Context agent</strong><select data-context aria-label="Context agent">${options}</select><span>Cost: all agents</span></div>
        </div>
        <div class="tl-scroll"><div class="tl-inner">
          <div class="timeline-plot"><div class="tl-axis">${axis}</div>${tracks}${markers}</div>
          <details class="usage-details" data-usage${graphsOpen ? ' open' : ''}><summary>Graphs and cost tables</summary><div class="native-graphs"></div></details>
        </div></div>
      </div>
      <details class="activity-history"><summary>Activity history <span>${all.length.toLocaleString()} events · all agents</span></summary><div class="activity-rows">${all.map(e => historyRow(e, first)).join('')}</div></details>
    </section>`;

    const tl = root.querySelector('.tl'), $ = s => tl.querySelector(s);
    const rerender = () => render(root, lanes, $('[data-idle]').checked, $('[data-context]').value);
    $('[data-idle]').addEventListener('change', rerender);
    $('[data-context]').addEventListener('change', rerender);
    $('[data-zoom]').addEventListener('change', e => { $('.tl-inner').style.width = `${Number(e.target.value) * 100}%`; });

    // The chat graph mounts on first expansion. A stale toggle from a replaced
    // render must not mount into a detached node.
    const usage = $('[data-usage]'), graphs = $('.native-graphs');
    const showGraphs = () => {
      if (usage.open && usage.isConnected && !graphs.hasChildNodes())
        TimelineGraphs.mount(graphs, lanes, contextId, x, compactions.map(c => c.x), showIdle ? 'Elapsed time · shared with timeline' : 'Active time · shared with timeline');
    };
    usage.addEventListener('toggle', showGraphs);
    showGraphs();

    tl.querySelectorAll('.raw-input').forEach(details => details.addEventListener('toggle', () => {
      const pre = details.querySelector('pre');
      if (details.open && !pre.textContent) pre.textContent = JSON.stringify(all[details.dataset.index].input, null, 2);
    }));

    const buttons = [...tl.querySelectorAll('[data-kind-filter]')], blocks = [...tl.querySelectorAll('.blk')], selected = new Set();
    const focus = kind => blocks.forEach(b => { b.style.opacity = kind && b.dataset.kind !== kind ? '.14' : ''; });
    function filter() {
      buttons.forEach(b => b.setAttribute('aria-pressed', selected.has(b.dataset.kindFilter)));
      blocks.forEach(b => { b.hidden = selected.size > 0 && !selected.has(b.dataset.kind); });
      $('.legend-clear').disabled = !selected.size;
    }
    for (const b of buttons) {
      const kind = b.dataset.kindFilter;
      b.addEventListener('mouseenter', () => focus(kind));
      b.addEventListener('mouseleave', () => focus(''));
      b.addEventListener('focus', () => focus(kind));
      b.addEventListener('blur', () => focus(''));
      b.addEventListener('click', () => { selected.has(kind) ? selected.delete(kind) : selected.add(kind); filter(); });
    }
    $('.legend-clear').addEventListener('click', () => { selected.clear(); filter(); focus(''); });

    for (const b of blocks) b.addEventListener('click', () => {
      const row = $(`#activity-${b.dataset.index}`), open = !row.open;
      tl.querySelectorAll('.activity-row[open]').forEach(r => r.open = false);
      blocks.forEach(block => { block.classList.remove('sel'); block.setAttribute('aria-expanded', 'false'); });
      if (!open) return;
      $('.activity-history').open = true;
      row.open = true;
      b.classList.add('sel');
      b.setAttribute('aria-expanded', 'true');
      row.scrollIntoView({block: 'nearest', inline: 'nearest'});
    });
  }
  window.ShelleyTimeline = {render};
})();
