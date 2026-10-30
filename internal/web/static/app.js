/* =========================================================================
   playpile — app.js
   Vanilla JS progressive enhancement. No dependencies, no build step.
   Every feature degrades gracefully: with JS off, the plain forms still
   post and redirect normally.
   ========================================================================= */
(function () {
  "use strict";

  /* ---------------------------------------------------------------
   * Flash helper: show an error either in the page flash slot, or
   * fall back to alert() if that slot does not exist.
   * ------------------------------------------------------------- */
  function showError(message) {
    var text = message || "Something went wrong.";
    var slot = document.getElementById("flash-slot");
    if (slot) {
      slot.innerHTML = "";
      var div = document.createElement("div");
      div.className = "flash flash--error";
      div.textContent = text;
      slot.appendChild(div);
    } else {
      alert(text);
    }
  }

  function closestEntry(el) {
    if (!el || typeof el.closest !== "function") return null;
    return el.closest("article.entry");
  }

  function setBusy(entry, busy) {
    if (!entry) return;
    if (busy) {
      entry.classList.add("is-busy");
    } else {
      entry.classList.remove("is-busy");
    }
  }

  /* A hard no disqualifies a row: mirror the server's is-vetoed class and
   * the chip without reloading. The row only changes position on reload. */
  function setVetoed(entry, vetoed, downCount) {
    if (!entry) return;
    entry.classList.toggle("is-vetoed", !!vetoed);
    var meta = entry.querySelector(".entry-meta");
    if (!meta) return;
    var chip = meta.querySelector(".chip--veto");
    if (vetoed) {
      if (!chip) {
        chip = document.createElement("span");
        chip.className = "chip chip--veto";
        chip.setAttribute("title", "a hard no disqualifies a game");
        meta.appendChild(chip);
      }
      chip.textContent =
        (typeof downCount === "number" ? downCount : "") + " hard no — out";
    } else if (chip) {
      chip.remove();
    }
  }

  /* ---------------------------------------------------------------
   * Apply a JSON update response to the page: summary, vote score,
   * owners, "mine" line, or a full reload.
   * ------------------------------------------------------------- */
  function applyUpdate(data, form) {
    var recognised = false;

    if (data.summary_html) {
      var summary = document.getElementById("summary");
      if (summary) {
        summary.innerHTML = data.summary_html;
      }
      recognised = true;
    }

    var entry = null;
    var entryId = form.getAttribute("data-entry");
    if (entryId) {
      entry = document.querySelector(
        'article.entry[data-entry="' + CSS.escape(entryId) + '"]'
      );
    }
    if (!entry) {
      entry = closestEntry(form);
    }

    if (typeof data.score !== "undefined" && entry) {
      var scoreEl = entry.querySelector(".vote-score");
      if (scoreEl) {
        scoreEl.textContent = data.score;
        var sign = "zero";
        if (data.score > 0) sign = "pos";
        else if (data.score < 0) sign = "neg";
        scoreEl.setAttribute("data-sign", sign);
      }
      var up = entry.querySelector(".vote-btn.up");
      var down = entry.querySelector(".vote-btn.down");
      var mine = data.mine;
      if (up) up.setAttribute("aria-pressed", mine === 1 ? "true" : "false");
      if (down) {
        down.setAttribute("aria-pressed", mine === -1 ? "true" : "false");
      }
      if (typeof data.vetoed !== "undefined") {
        setVetoed(entry, data.vetoed, data.down);
      }
      recognised = true;
    }

    if (data.owners_html && entry) {
      var owners = entry.querySelector(".owners");
      if (owners) {
        owners.outerHTML = data.owners_html;
      }
      recognised = true;
    }

    if (data.mine_html && entry) {
      var mineEl = entry.querySelector(".entry-mine");
      if (mineEl) {
        mineEl.outerHTML = data.mine_html;
      }
      recognised = true;
    }

    if (data.reload || !recognised) {
      location.reload();
    }
  }

  /* ---------------------------------------------------------------
   * 1 & 2. form[data-ajax] interception, including vote toggling.
   * ------------------------------------------------------------- */
  document.addEventListener(
    "click",
    function (event) {
      var btn = event.target.closest && event.target.closest(".vote-btn");
      if (!btn) return;
      var form = btn.closest("form");
      if (!form) return;
      var pressed = btn.getAttribute("aria-pressed") === "true";
      var valueInput = form.querySelector('input[name="value"]');
      if (pressed && valueInput) {
        valueInput.value = "0";
      } else if (valueInput) {
        valueInput.value = btn.classList.contains("up") ? "1" : "-1";
      }
    },
    true
  );

  document.addEventListener("submit", function (event) {
    var form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    if (!form.hasAttribute("data-ajax")) return;

    event.preventDefault();

    var entry = closestEntry(form);
    var entryId = form.getAttribute("data-entry");
    if (entryId && !entry) {
      entry = document.querySelector(
        'article.entry[data-entry="' + CSS.escape(entryId) + '"]'
      );
    }
    setBusy(entry, true);

    var formData = new URLSearchParams(new FormData(form));

    fetch(form.getAttribute("action") || form.action, {
      method: form.getAttribute("method") || "POST",
      body: formData,
      credentials: "same-origin",
      headers: {
        "X-Playpile": "fetch",
        Accept: "application/json",
      },
    })
      .then(function (response) {
        return response
          .json()
          .catch(function () {
            return { ok: false, error: "Unexpected response." };
          })
          .then(function (data) {
            return { response: response, data: data };
          });
      })
      .then(function (result) {
        var ok = result.response.ok && result.data && result.data.ok;
        if (!ok) {
          showError(
            (result.data && result.data.error) ||
              "The request failed. Please try again."
          );
          return;
        }
        applyUpdate(result.data, form);
      })
      .catch(function () {
        showError("Network error. Please try again.");
      })
      .finally(function () {
        setBusy(entry, false);
      });
  });

  /* ---------------------------------------------------------------
   * 3. Search box with debounce, dropdown results, add-on-click.
   * ------------------------------------------------------------- */
  var searchInputs = document.querySelectorAll("input.search-input");

  searchInputs.forEach(function (input) {
    var form = input.closest("form");
    var results = form
      ? form.querySelector(".search-results")
      : input.parentElement
      ? input.parentElement.querySelector(".search-results")
      : null;
    if (!results) return;

    // The URL lives on the input, and on the form as a fallback: an empty
    // searchUrl used to silently disable the whole dropdown.
    var searchUrl =
      input.getAttribute("data-search-url") ||
      (form ? form.getAttribute("data-search-url") : null);
    var addUrl =
      input.getAttribute("data-add-url") ||
      (form ? form.getAttribute("data-add-url") : null);
    if (!searchUrl) return;
    var debounceTimer = null;
    var sequence = 0;

    function closeResults() {
      ++sequence;
      if (debounceTimer) clearTimeout(debounceTimer);
      results.classList.remove("is-open");
      results.innerHTML = "";
    }

    function renderResults(list) {
      results.innerHTML = "";
      if (!list || list.length === 0) {
        closeResults();
        return;
      }
      list.forEach(function (item) {
        var row = document.createElement("button");
        row.type = "button";
        row.className = "result";
        row.setAttribute("data-appid", item.appid);

        var img = document.createElement("img");
        img.className = "result-cover";
        img.src = item.image || "";
        img.alt = "";
        row.appendChild(img);

        var name = document.createElement("span");
        name.className = "result-name";
        name.textContent = item.name || "";
        row.appendChild(name);

        var meta = document.createElement("span");
        meta.className = "result-meta";

        var price = document.createElement("span");
        price.className = "result-price num";
        price.textContent = item.price || "";
        meta.appendChild(price);

        var score = document.createElement("span");
        score.className = "result-score num";
        score.textContent = item.meta || "";
        meta.appendChild(score);

        row.appendChild(meta);

        row.addEventListener("click", function () {
          if (!addUrl) return;
          row.disabled = true;
          fetch(addUrl, {
            method: "POST",
            credentials: "same-origin",
            headers: {
              "X-Playpile": "fetch",
              Accept: "application/json",
              "Content-Type": "application/json",
            },
            body: JSON.stringify({ appid: item.appid }),
          })
            .then(function (response) {
              return response
                .json()
                .catch(function () {
                  return { ok: false };
                })
                .then(function (data) {
                  return { response: response, data: data };
                });
            })
            .then(function (result) {
              if (result.response.ok && result.data && result.data.ok) {
                location.reload();
              } else {
                row.disabled = false;
                showError(
                  (result.data && result.data.error) || "Could not add game."
                );
              }
            })
            .catch(function () {
              row.disabled = false;
              showError("Network error. Please try again.");
            });
        });

        results.appendChild(row);
      });
      results.classList.add("is-open");
    }

    input.addEventListener("input", function () {
      var term = input.value.trim();
      var mySeq = ++sequence;
      if (debounceTimer) clearTimeout(debounceTimer);
      if (term.length < 2) {
        closeResults();
        return;
      }
      debounceTimer = setTimeout(function () {
        fetch(searchUrl + "?q=" + encodeURIComponent(term), {
          credentials: "same-origin",
          headers: { Accept: "application/json" },
        })
          .then(function (response) {
            return response.json().then(function (data) {
              if (!response.ok) throw new Error(data.error || "Steam search failed.");
              return data;
            });
          })
          .then(function (data) {
            if (mySeq !== sequence) return;
            renderResults(data && data.results);
          })
          .catch(function (error) {
            if (mySeq !== sequence) return;
            closeResults();
            showError(error.message || "Steam search failed. Please try again.");
          });
      }, 250);
    });

    input.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        closeResults();
      }
    });

    document.addEventListener("click", function (event) {
      if (!results.classList.contains("is-open")) return;
      var within =
        results.contains(event.target) || input.contains(event.target);
      if (!within) closeResults();
    });
  });

  /* ---------------------------------------------------------------
   * 4. [data-copy] buttons: copy text, flash "copied" for 1.2s.
   * ------------------------------------------------------------- */
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var value = btn.getAttribute("data-copy") || "";
      if (!navigator.clipboard || !navigator.clipboard.writeText) return;
      navigator.clipboard
        .writeText(value)
        .then(function () {
          var original = btn.textContent;
          btn.textContent = "copied";
          setTimeout(function () {
            btn.textContent = original;
          }, 1200);
        })
        .catch(function () {
          /* Clipboard access denied; nothing else to do. */
        });
    });
  });

  /* ---------------------------------------------------------------
   * 5. form[data-confirm]: confirm() before submitting.
   * ------------------------------------------------------------- */
  document.addEventListener(
    "submit",
    function (event) {
      var form = event.target;
      if (!(form instanceof HTMLFormElement)) return;
      var message = form.getAttribute("data-confirm");
      if (!message) return;
      if (!confirm(message)) {
        event.preventDefault();
        event.stopPropagation();
      }
    },
    true
  );

  /* ---------------------------------------------------------------
   * 6. Keyboard: "/" focuses the search box, unless already typing.
   * ------------------------------------------------------------- */
  document.addEventListener("keydown", function (event) {
    if (event.key !== "/") return;
    var active = document.activeElement;
    var tag = active ? active.tagName : "";
    var isTyping =
      tag === "INPUT" ||
      tag === "TEXTAREA" ||
      tag === "SELECT" ||
      (active && active.isContentEditable);
    if (isTyping) return;
    var target = document.querySelector(".search-input");
    if (!target) return;
    event.preventDefault();
    target.focus();
  });
})();
