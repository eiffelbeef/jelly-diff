/* jelly-diff — minimal vanilla JS */


// Highlight current nav link
(function () {
  var path = window.location.pathname.split('?')[0];
  document.querySelectorAll('.nav-link').forEach(function (a) {
    var href = a.getAttribute('href');
    if (href === path || (path === '/' && href === '/') || (path.startsWith('/stats') && href === '/stats')) {
      a.classList.add('active');
    }
  });
})();

// Sync Now button & dynamic feed updater
(function () {
  var btn = document.getElementById('syncBtn');
  if (!btn) return;

  var pollInterval = null;
  var pollTimeout = null;

  function setSyncLabel(text) {
    var label = btn.querySelector('.sync-label');
    if (label) {
      label.textContent = text;
    }
  }

  function resetSyncButton() {
    btn.classList.remove('syncing');
    btn.classList.remove('cooldown');
    setSyncLabel('Sync Now');
  }

  function updateLastSync(doc) {
    var curLastSync = document.getElementById('lastSync');
    var newLastSync = doc ? doc.getElementById('lastSync') : null;
    if (curLastSync && newLastSync) {
      curLastSync.textContent = newLastSync.textContent;
    } else if (!curLastSync && newLastSync) {
      var newSubHeader = doc.querySelector('.sub-header');
      var header = document.querySelector('.site-header');
      if (newSubHeader && header) {
        header.insertAdjacentElement('afterend', newSubHeader);
      }
    } else if (curLastSync) {
      curLastSync.textContent = 'Last sync: just now';
    } else {
      var headerElem = document.querySelector('.site-header');
      if (headerElem) {
        var subHeader = document.createElement('div');
        subHeader.className = 'sub-header';
        subHeader.innerHTML = '<div class="container sub-header-container"><span class="sync-status" id="lastSync">Last sync: just now</span></div>';
        headerElem.insertAdjacentElement('afterend', subHeader);
      }
    }
  }

  function updateFeed() {
    return fetch(window.location.href, {
      headers: {
        'Accept': 'text/html',
        'X-Requested-With': 'XMLHttpRequest'
      }
    })
    .then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.text();
    })
    .then(function (html) {
      var parser = new DOMParser();
      var doc = parser.parseFromString(html, 'text/html');

      // Update feed main section if present on current page
      var curFeed = document.querySelector('.feed-main');
      var newFeed = doc.querySelector('.feed-main');
      if (curFeed && newFeed) {
        var oldIds = new Set();
        curFeed.querySelectorAll('.event-card-link').forEach(function (a) {
          var href = a.getAttribute('href') || '';
          var m = href.match(/\/events\/(\d+)/);
          if (m) oldIds.add(m[1]);
        });

        curFeed.innerHTML = newFeed.innerHTML;

        curFeed.querySelectorAll('.event-card-link').forEach(function (a) {
          var href = a.getAttribute('href') || '';
          var m = href.match(/\/events\/(\d+)/);
          if (m && !oldIds.has(m[1])) {
            var card = a.closest('.event-card');
            if (card) {
              card.classList.add('event-card--new');
            }
          }
        });
      }

      // Update library dropdown in filter sidebar if present
      var curLib = document.getElementById('library');
      var newLib = doc.getElementById('library');
      if (curLib && newLib) {
        var curVal = curLib.value;
        curLib.innerHTML = newLib.innerHTML;
        curLib.value = curVal;
      }

      // Update stats page if present on current page
      var curStats = document.querySelector('.stats-page');
      var newStats = doc.querySelector('.stats-page');
      if (curStats && newStats) {
        curStats.innerHTML = newStats.innerHTML;
      }

      // Update Last sync in sub-header
      updateLastSync(doc);
    });
  }

  function startPolling(startSeq) {
    if (pollInterval) clearInterval(pollInterval);
    if (pollTimeout) clearTimeout(pollTimeout);

    var wasSyncingSeen = false;

    pollInterval = setInterval(function () {
      fetch('/api/sync')
        .then(function (res) {
          if (!res.ok) throw new Error('Status ' + res.status);
          return res.json();
        })
        .then(function (data) {
          if (data.is_syncing) {
            wasSyncingSeen = true;
            setSyncLabel('Syncing…');
            return;
          }

          var finished = false;
          if (startSeq !== null && startSeq !== undefined) {
            finished = (data.sync_seq > startSeq);
          } else {
            finished = wasSyncingSeen || !data.is_syncing;
          }

          if (finished) {
            clearInterval(pollInterval);
            pollInterval = null;
            if (pollTimeout) clearTimeout(pollTimeout);

            var newCount = data.last_sync_events || 0;
            if (newCount > 0) {
              updateFeed().catch(function (err) {
                console.error('Failed to update feed:', err);
              });
              btn.classList.remove('syncing');
              setSyncLabel(newCount === 1 ? '1 new event' : newCount + ' new events');
              setTimeout(resetSyncButton, 3000);
            } else {
              updateLastSync();
              btn.classList.remove('syncing');
              setSyncLabel('Up to date');
              setTimeout(resetSyncButton, 2000);
            }
          }
        })
        .catch(function () {
          // Fallback to /api/stats
          fetch('/api/stats')
            .then(function (r) { return r.json(); })
            .then(function (statsData) {
              var isSyncing = statsData.IsSyncing || statsData.is_syncing;
              if (isSyncing) {
                wasSyncingSeen = true;
                return;
              }
              var seq = statsData.SyncSeq || statsData.sync_seq || 0;
              var finished = (startSeq !== null && startSeq !== undefined) ? (seq > startSeq) : wasSyncingSeen;
              if (finished) {
                clearInterval(pollInterval);
                pollInterval = null;
                if (pollTimeout) clearTimeout(pollTimeout);

                var count = statsData.LastSyncEvents || statsData.last_sync_events || 0;
                if (count > 0) {
                  updateFeed().catch(function (err) {
                    console.error('Failed to update feed:', err);
                  });
                  btn.classList.remove('syncing');
                  setSyncLabel(count === 1 ? '1 new event' : count + ' new events');
                  setTimeout(resetSyncButton, 3000);
                } else {
                  updateLastSync();
                  btn.classList.remove('syncing');
                  setSyncLabel('Up to date');
                  setTimeout(resetSyncButton, 2000);
                }
              }
            })
            .catch(function () {});
        });
    }, 1500);

    pollTimeout = setTimeout(function () {
      if (pollInterval) clearInterval(pollInterval);
      pollInterval = null;
      resetSyncButton();
    }, 60000);
  }

  btn.addEventListener('click', function () {
    if (btn.classList.contains('syncing')) return;

    var csrf = btn.dataset.csrf || '';
    btn.classList.remove('cooldown');
    btn.classList.add('syncing');
    setSyncLabel('Syncing…');

    fetch('/api/sync', {
      method: 'POST',
      headers: {
        'X-CSRF-Token': csrf,
        'Content-Type': 'application/json'
      }
    })
    .then(function (res) {
      if (!res.ok) {
        return res.json().catch(function () {
          return { error: 'Request failed with status ' + res.status };
        }).then(function (errBody) {
          throw { status: res.status, body: errBody };
        });
      }
      return res.json();
    })
    .then(function (data) {
      var seq = data && data.sync_seq !== undefined ? data.sync_seq : null;
      startPolling(seq);
    })
    .catch(function (err) {
      if (err.status === 409) {
        setSyncLabel('Sync in progress…');
        var seq = err.body && err.body.sync_seq !== undefined ? err.body.sync_seq : null;
        startPolling(seq);
      } else {
        btn.classList.remove('syncing');
        if (err.status === 429) {
          var wait = err.body && err.body.retry_after_seconds ? err.body.retry_after_seconds + 's' : 'active';
          btn.classList.add('cooldown');
          setSyncLabel('Cooldown (' + wait + ')');
          setTimeout(resetSyncButton, 3000);
        } else if (err.status === 401) {
          setSyncLabel('Unauthorized');
          setTimeout(function () {
            window.location.href = '/login';
          }, 1200);
        } else {
          setSyncLabel('Error');
          setTimeout(resetSyncButton, 3000);
        }
      }
    });
  });
})();

// Color theme toggle (auto -> light -> dark -> auto)
(function () {
  var toggleBtn = document.getElementById('themeToggle');
  if (!toggleBtn) return;

  var TITLES = {
    auto: 'Theme: Auto (system)',
    light: 'Theme: Light',
    dark: 'Theme: Dark'
  };

  var ORDER = ['auto', 'light', 'dark'];

  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);
    document.cookie = 'theme=' + theme + ';path=/;max-age=31536000;SameSite=Lax';
    toggleBtn.title = TITLES[theme] || TITLES.auto;
    toggleBtn.setAttribute('aria-label', TITLES[theme] || TITLES.auto);
  }

  var current = localStorage.getItem('theme') || 'auto';
  if (current !== 'auto' || document.documentElement.getAttribute('data-theme') !== current) {
    applyTheme(current);
  }

  toggleBtn.addEventListener('click', function () {
    var cur = document.documentElement.getAttribute('data-theme') || 'auto';
    var idx = ORDER.indexOf(cur);
    if (idx === -1) idx = 0;
    var next = ORDER[(idx + 1) % ORDER.length];
    applyTheme(next);
  });
})();
