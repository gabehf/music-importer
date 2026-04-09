'use strict';

// IDs of fetch cards we've already created, so we don't duplicate them.
const knownFetchIds = new Set();

document.addEventListener('DOMContentLoaded', () => {
  initTabs();
  initSearch();
  initFetchList();
});

// ── Tabs ───────────────────────────────────────────────────────────────────────

function initTabs() {
  document.querySelector('.tabs').addEventListener('click', e => {
    const btn = e.target.closest('.tab-btn');
    if (!btn) return;
    showTab(btn.dataset.tab);
  });
}

function showTab(name) {
  document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  document.getElementById('tab-' + name).classList.add('active');
  document.querySelector(`.tab-btn[data-tab="${name}"]`).classList.add('active');
}

// ── Search ─────────────────────────────────────────────────────────────────────

let searchType = 'release';

function initSearch() {
  document.querySelector('.type-toggle').addEventListener('click', e => {
    const btn = e.target.closest('.type-btn');
    if (btn) setSearchType(btn.dataset.type);
  });

  const searchBtn = document.getElementById('search-btn');
  const searchInput = document.getElementById('search-q');
  searchBtn.addEventListener('click', doSearch);
  searchInput.addEventListener('keydown', e => { if (e.key === 'Enter') doSearch(); });

  // Event delegation for dynamically rendered result buttons
  document.getElementById('search-results').addEventListener('click', e => {
    const btn = e.target.closest('.fetch-btn');
    if (!btn || btn.disabled) return;
    if (btn.dataset.fetchType === 'artist') startArtistFetch(btn);
    else startReleaseFetch(btn);
  });
}

function setSearchType(type) {
  searchType = type;
  document.querySelectorAll('.type-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.type === type);
  });
}

function doSearch() {
  const q = document.getElementById('search-q').value.trim();
  if (!q) return;

  const btn = document.getElementById('search-btn');
  const resultsEl = document.getElementById('search-results');

  btn.disabled = true;
  btn.textContent = 'Searching\u2026';
  resultsEl.innerHTML = '<p class="search-msg">Searching MusicBrainz\u2026</p>';

  fetch(`/discover/search?q=${encodeURIComponent(q)}&type=${searchType}`)
    .then(r => {
      if (!r.ok) return r.text().then(t => { throw new Error(t || r.statusText); });
      return r.json();
    })
    .then(data => renderResults(data))
    .catch(err => {
      resultsEl.innerHTML = `<p class="search-msg error">Error: ${esc(err.message)}</p>`;
    })
    .finally(() => {
      btn.disabled = false;
      btn.textContent = 'Search';
    });
}

// ── Results rendering ──────────────────────────────────────────────────────────

function renderResults(data) {
  const el = document.getElementById('search-results');
  if (!data || data.length === 0) {
    el.innerHTML = '<p class="search-msg">No results found.</p>';
    return;
  }
  const renderer = searchType === 'artist' ? renderArtist : renderRelease;
  el.innerHTML = data.map(renderer).join('');
}

function renderRelease(r) {
  const credits = r['artist-credit'] ?? [];
  const artist = credits.map(c => c.name || c.artist?.name || '').join('') || 'Unknown Artist';
  const year = r.date?.substring(0, 4) ?? '';
  const type = r['release-group']?.['primary-type'] ?? '';
  const country = r.country ?? '';
  const formats = [...new Set((r.media ?? []).map(m => m.format).filter(Boolean))].join('+');
  const meta = [year, type, formats, country].filter(Boolean).join(' \u00b7 ');
  const coverUrl = `https://coverartarchive.org/release/${r.id}/front-250`;

  return `
    <div class="result-row">
      <img class="result-cover" src="${coverUrl}" onerror="this.style.display='none'" loading="lazy" alt="">
      <div class="result-info">
        <span class="result-title">${esc(artist)} \u2014 ${esc(r.title)}</span>
        ${meta ? `<span class="result-meta">${esc(meta)}</span>` : ''}
      </div>
      <button class="fetch-btn"
        data-fetch-type="release"
        data-id="${esc(r.id)}"
        data-artist="${esc(artist)}"
        data-album="${esc(r.title)}">Fetch</button>
    </div>`;
}

function renderArtist(a) {
  const dis = a.disambiguation ? ` (${esc(a.disambiguation)})` : '';
  return `
    <div class="result-row">
      <div class="result-info">
        <span class="result-title">${esc(a.name)}${dis}</span>
        ${a.country ? `<span class="result-meta">${esc(a.country)}</span>` : ''}
      </div>
      <button class="fetch-btn"
        data-fetch-type="artist"
        data-id="${esc(a.id)}"
        data-name="${esc(a.name)}">Fetch All</button>
    </div>`;
}

// ── Fetch operations ───────────────────────────────────────────────────────────

function startReleaseFetch(btn) {
  const { id, artist, album } = btn.dataset;
  btn.disabled = true;
  btn.textContent = 'Fetching\u2026';

  fetch('/discover/fetch', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, artist, album }),
  })
    .then(r => {
      if (!r.ok) return r.text().then(t => { throw new Error(t || r.statusText); });
      return r.json();
    })
    .then(() => {
      addFetchCard(id, `${artist} \u2014 ${album}`);
      pollFetch(id);
    })
    .catch(err => {
      btn.disabled = false;
      btn.textContent = 'Fetch';
      showFetchError(err.message);
    });
}

function startArtistFetch(btn) {
  const { id, name } = btn.dataset;
  btn.disabled = true;
  btn.textContent = 'Fetching\u2026';

  fetch('/discover/fetch/artist', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, name }),
  })
    .then(r => {
      if (!r.ok) return r.text().then(t => { throw new Error(t || r.statusText); });
      return r.json();
    })
    .then(() => {
      addFetchCard(id, `${name} \u2014 full discography`);
      pollFetch(id);
    })
    .catch(err => {
      btn.disabled = false;
      btn.textContent = 'Fetch All';
      showFetchError(err.message);
    });
}

// ── Fetch cards ────────────────────────────────────────────────────────────────

function addFetchCard(id, title) {
  knownFetchIds.add(id);
  const list = document.getElementById('fetch-list');
  const card = document.createElement('div');
  card.className = 'fetch-card';
  card.id = `fetch-${id}`;
  card.innerHTML = `
    <div class="fetch-header">
      <span class="fetch-title">${esc(title)}</span>
      <span class="fetch-status" id="fstatus-${id}">In progress\u2026</span>
    </div>
    <div class="fetch-log" id="flog-${id}"></div>`;
  list.prepend(card);
}

function pollFetch(id) {
  fetch(`/discover/fetch/status?id=${encodeURIComponent(id)}`)
    .then(r => r.json())
    .then(data => {
      const logEl    = document.getElementById(`flog-${id}`);
      const statusEl = document.getElementById(`fstatus-${id}`);
      const card     = document.getElementById(`fetch-${id}`);

      if (logEl && data.log) {
        logEl.innerHTML = data.log
          .map(l => `<div class="log-line">${esc(l)}</div>`)
          .join('');
        logEl.scrollTop = logEl.scrollHeight;
      }

      if (data.done) {
        if (data.success) {
          statusEl?.setAttribute('class', 'fetch-status fetch-status-ok');
          if (statusEl) statusEl.textContent = '\u2713 done';
          card?.classList.add('fetch-card-ok');
        } else {
          statusEl?.setAttribute('class', 'fetch-status fetch-status-err');
          if (statusEl) statusEl.textContent = '\u2717 failed';
          card?.classList.add('fetch-card-err');
          if (data.error && logEl) {
            logEl.innerHTML += `<div class="log-line log-line-err">${esc(data.error)}</div>`;
            logEl.scrollTop = logEl.scrollHeight;
          }
        }
      } else {
        setTimeout(() => pollFetch(id), 2000);
      }
    })
    .catch(() => setTimeout(() => pollFetch(id), 3000));
}

// ── Fetch list polling ─────────────────────────────────────────────────────────

// Polls /discover/fetch/list every 5 s to discover server-created fetch entries
// (e.g. per-album cards spawned during an artist fetch) and create cards for them.
function initFetchList() {
  pollFetchList();
}

function pollFetchList() {
  fetch('/discover/fetch/list')
    .then(r => r.ok ? r.json() : null)
    .then(items => {
      if (!items) return;
      for (const item of items) {
        if (!knownFetchIds.has(item.id)) {
          knownFetchIds.add(item.id);
          addFetchCard(item.id, item.title);
          if (!item.done) pollFetch(item.id);
        }
      }
    })
    .catch(() => {})
    .finally(() => setTimeout(pollFetchList, 5000));
}

// ── Utilities ──────────────────────────────────────────────────────────────────

function showFetchError(msg) {
  const list = document.getElementById('fetch-list');
  const el = document.createElement('div');
  el.className = 'fetch-card fetch-card-err';
  el.innerHTML = `<div class="fetch-header">
    <span class="fetch-title">Fetch failed</span>
    <span class="fetch-status fetch-status-err">\u2717 error</span>
  </div>
  <div class="fetch-log"><div class="log-line log-line-err">${esc(msg)}</div></div>`;
  list.prepend(el);
}

function esc(s) {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}
