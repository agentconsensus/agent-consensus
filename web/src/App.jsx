import { useCallback, useEffect, useRef, useState } from "react";

const API_ROOT = "/api/v1";

function formatDate(value) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short"
  }).format(new Date(value));
}

function shortHash(value) {
  if (!value) return "—";
  const clean = value.replace(/^sha256:/, "");
  return clean.length > 12 ? `${clean.slice(0, 12)}…` : clean;
}

// Convert a git remote (including scp-style git@host:org/repo.git) into a
// browsable https URL. Returns "" when the remote is not a web URL.
function repositoryHref(value) {
  if (!value) return "";
  let href = String(value).trim();
  const scpLike = href.match(/^git@([^:]+):(.+)$/);
  if (scpLike) href = `https://${scpLike[1]}/${scpLike[2]}`;
  href = href.replace(/\.git$/, "");
  return /^https?:\/\//i.test(href) ? href : "";
}

function splitScope(value) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

// Navigation is URL-driven: ?org=slug selects an organization and
// ?org=slug&repo=name drills into one repository, so refreshes, shared links
// and browser back/forward all land on the same view.
function readLocation() {
  const params = new URLSearchParams(window.location.search);
  return { org: params.get("org") || "", repo: params.get("repo") || "" };
}

function writeLocation(org, repo, replace = false) {
  const params = new URLSearchParams();
  if (org) params.set("org", org);
  if (repo) params.set("repo", repo);
  const search = params.toString();
  const url = `${window.location.pathname}${search ? `?${search}` : ""}`;
  window.history[replace ? "replaceState" : "pushState"]({}, "", url);
}

// Older server state and interrupted requests should never make a dashboard
// render fail merely because an optional collection is absent or null.
function normalizeDetail(value) {
  return {
    ...value,
    repositories: asArray(value?.repositories),
    decisions: asArray(value?.decisions),
    rules: asArray(value?.rules ?? value?.policies),
    events: asArray(value?.events),
    proposals: asArray(value?.proposals),
    promotions: asArray(value?.promotions),
    context_records: asArray(value?.context_records)
  };
}

function sortDecisions(views) {
  return [...views].sort((a, b) => {
    if (a.repository !== b.repository) return a.repository.localeCompare(b.repository);
    return a.decision.id.localeCompare(b.decision.id);
  });
}

function Metric({ label, value, detail }) {
  return (
    <article className="metric">
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </article>
  );
}

function NoticeStack({ notices, onDismiss }) {
  if (!notices.length) return null;
  return (
    <div className="notice-stack" aria-live="polite">
      {notices.map((notice) => (
        <div className={`notice notice-${notice.kind}`} key={notice.id}>
          <span>{notice.message}</span>
          <button type="button" aria-label="Dismiss notification" onClick={() => onDismiss(notice.id)}>×</button>
        </div>
      ))}
    </div>
  );
}

function Screen({ eyebrow, title, children }) {
  return (
    <section className="panel screen">
      <p className="eyebrow">{eyebrow}</p>
      <h2>{title}</h2>
      {children}
    </section>
  );
}

function DecisionCard({ view, onPromote, onDelete, actionPending }) {
  const { decision, context_record_count: records, repository } = view;

  return (
    <article className={`decision-card status-${decision.status}`}>
      <div className="decision-top">
        <div>
          <div className="decision-id">{decision.id}</div>
          <h3>{decision.title}</h3>
        </div>
        <div className="decision-status">
          <span className={`status-badge status-${decision.status}`}>{decision.status}</span>
        </div>
      </div>
      <p className="decision-statement">{decision.statement}</p>
      <div className="decision-meta">
        <span className="source-badge" title="Repository that owns this decision">
          {repository || "unknown repository"}
        </span>
        {asArray(decision.scope).map((scope) => <span className="tag" key={scope}>{scope}</span>)}
        {decision.owner && <span>Owner: {decision.owner}</span>}
        <span>rev {decision.revision}</span>
        <span>{records} context record{records === 1 ? "" : "s"}</span>
        <span>Updated {formatDate(decision.updated_at)}</span>
      </div>
      {(onPromote || onDelete) && (
        <div className="button-row decision-actions">
          {onPromote && (
            <button className="button button-quiet" type="button" disabled={actionPending} onClick={() => onPromote(view)}>
              Promote to organization
            </button>
          )}
          {onDelete && (
            <button className="button button-danger" type="button" disabled={actionPending} onClick={() => onDelete(view)}>
              Delete
            </button>
          )}
        </div>
      )}
    </article>
  );
}

function PromotionReviewCard({ view, onReview, pending }) {
  const { promotion, decision } = view;
  const [draft, setDraft] = useState(() => ({
    title: decision.title || "",
    statement: decision.statement || "",
    scope: asArray(decision.scope).join(", "),
    reviewNote: promotion.review_note || ""
  }));

  useEffect(() => {
    setDraft({
      title: decision.title || "",
      statement: decision.statement || "",
      scope: asArray(decision.scope).join(", "),
      reviewNote: promotion.review_note || ""
    });
  }, [promotion.uid, decision.title, decision.statement, promotion.review_note, decision.scope]);

  function update(field, value) {
    setDraft((current) => ({ ...current, [field]: value }));
  }

  async function review(status) {
    await onReview(promotion, {
      status,
      title: draft.title.trim(),
      statement: draft.statement.trim(),
      scope: splitScope(draft.scope),
      review_note: draft.reviewNote.trim()
    });
  }

  return (
    <article className="promotion-card">
      <div className="decision-top">
        <div>
          <div className="decision-id">{promotion.id} · {promotion.repository || "unknown repository"}</div>
          <h3>{decision.title}</h3>
        </div>
        <span className="status-badge status-pending">Pending review</span>
      </div>
      <p className="promotion-caption">Repository decision {promotion.decision_id}, submitted {formatDate(promotion.submitted_at || promotion.created_at)}. Edit the organization rule below before approving or rejecting it.</p>
      <form className="promotion-form" onSubmit={(event) => { event.preventDefault(); review("approved"); }}>
        <label>Title <input required maxLength="160" value={draft.title} onChange={(event) => update("title", event.target.value)} /></label>
        <label>Statement <textarea required maxLength="1000" value={draft.statement} onChange={(event) => update("statement", event.target.value)} /></label>
        <label>Scope <input required value={draft.scope} onChange={(event) => update("scope", event.target.value)} placeholder="backend, frontend" /></label>
        <label>Review note <textarea maxLength="1000" value={draft.reviewNote} onChange={(event) => update("reviewNote", event.target.value)} placeholder="Explain the decision or rejection." /></label>
        <div className="button-row">
          <button className="button" disabled={pending} type="submit">{pending ? "Saving…" : "Approve as organization rule"}</button>
          <button className="button button-danger" disabled={pending} type="button" onClick={() => review("rejected")}>Reject promotion</button>
        </div>
      </form>
    </article>
  );
}

function ProposalReviewCard({ proposal, onReview, pending }) {
  const [draft, setDraft] = useState(() => ({
    title: proposal.title || "",
    statement: proposal.statement || "",
    scope: asArray(proposal.scope).join(", "),
    owner: proposal.owner || "",
    reviewNote: proposal.review_note || ""
  }));
  useEffect(() => setDraft({
    title: proposal.title || "", statement: proposal.statement || "",
    scope: asArray(proposal.scope).join(", "), owner: proposal.owner || "", reviewNote: proposal.review_note || ""
  }), [proposal.uid, proposal.title, proposal.statement, proposal.scope, proposal.owner, proposal.review_note]);
  const update = (field, value) => setDraft((current) => ({ ...current, [field]: value }));
  const review = (status) => onReview(proposal, {
    status, title: draft.title.trim(), statement: draft.statement.trim(), scope: splitScope(draft.scope), owner: draft.owner.trim(), review_note: draft.reviewNote.trim()
  });
  return (
    <article className="promotion-card">
      <div className="decision-top"><div><div className="decision-id">{proposal.id} · {proposal.repository || "unknown repository"}</div><h3>{proposal.title}</h3></div><span className="status-badge status-pending">Pending review</span></div>
      <p className="promotion-caption">Temporary local decision. Approval creates a decision only in {proposal.repository || "its source repository"}; it is not an organization rule.</p>
      <form className="promotion-form" onSubmit={(event) => { event.preventDefault(); review("approved"); }}>
        <label>Title <input required maxLength="160" value={draft.title} onChange={(event) => update("title", event.target.value)} /></label>
        <label>Statement <textarea required maxLength="1000" value={draft.statement} onChange={(event) => update("statement", event.target.value)} /></label>
        <div className="two-columns"><label>Scope <input required value={draft.scope} onChange={(event) => update("scope", event.target.value)} /></label><label>Owner <input maxLength="120" value={draft.owner} onChange={(event) => update("owner", event.target.value)} /></label></div>
        <label>Review note <textarea maxLength="1000" value={draft.reviewNote} onChange={(event) => update("reviewNote", event.target.value)} /></label>
        <div className="button-row"><button className="button" disabled={pending} type="submit">{pending ? "Saving…" : "Approve as repository decision"}</button><button className="button button-danger" disabled={pending} type="button" onClick={() => review("rejected")}>Reject</button></div>
      </form>
    </article>
  );
}

function ContextItemList({ items }) {
  const safeItems = asArray(items);
  if (!safeItems.length) return <p className="empty-copy">None in scope.</p>;
  return (
    <ul className="context-item-list">
      {safeItems.map((item) => (
        <li key={item.id}>
          <div className="context-item-head">
            <span className="decision-id">{item.id}</span>
            <strong>{item.title}</strong>
          </div>
          <p>{item.statement}</p>
          {item.scope.length > 0 && (
            <div className="decision-meta">{item.scope.map((scope) => <span className="tag" key={scope}>{scope}</span>)}</div>
          )}
        </li>
      ))}
    </ul>
  );
}

function ContextPreview({ onPreview, onPreviewMarkdown, pending, repositories, repository }) {
  const [document, setDocument] = useState(null);
  const [view, setView] = useState("structured");
  const [markdown, setMarkdown] = useState("");
  const fixedRepository = Boolean(repository);

  async function submit(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const name = fixedRepository ? repository : String(form.get("repository") || "");
    const role = String(form.get("role") || "");
    const agent = String(form.get("agent") || "");
    // Fetch the structured document and the raw injection fragment together
    // so switching views afterwards is instant.
    const [compiled, raw] = await Promise.all([
      onPreview(name, role, agent),
      onPreviewMarkdown(name, role, agent)
    ]);
    if (compiled) {
      setDocument(compiled);
      setMarkdown(raw || "");
      setView("structured");
    }
  }

  function viewButton(name, label) {
    return (
      <button
        className={`link-button${view === name ? " view-active" : ""}`}
        type="button"
        aria-pressed={view === name}
        onClick={() => setView(name)}
      >
        {label}
      </button>
    );
  }

  return (
    <article className="panel context-panel">
      <div className="section-heading">
        <div><p className="eyebrow">RUNTIME</p><h2>Context preview</h2></div>
      </div>
      <form className="context-form" onSubmit={submit}>
        {!fixedRepository && (
          <label>Repository
            <select name="repository" defaultValue="">
              <option value="">Whole organization</option>
              {repositories.map((repository) => (
                <option value={repository.name} key={repository.name}>{repository.name}</option>
              ))}
            </select>
          </label>
        )}
        <div className="two-columns">
          <label>Role <input name="role" placeholder="all" /></label>
          <label>Agent <input name="agent" placeholder="codex" /></label>
        </div>
        <button className="button button-quiet" disabled={pending} type="submit">
          {pending ? "Compiling…" : "Compile context"}
        </button>
      </form>
      {!document ? (
        <p className="empty-copy">Compile to see exactly which repository decisions, organization rules, and cases an agent receives. This is read-only and does not create an audit record.</p>
      ) : (
        <div className="context-result">
          <div className="context-result-meta">
            <span>Repository: {document.repository || "all"}</span>
            <span>Role: {document.role || "all"}</span>
            <span>Agent: {document.agent || "—"}</span>
            <span title={document.context_hash}>Hash: {shortHash(document.context_hash)}</span>
            <span className="view-switch">
              {viewButton("structured", "Structured")}
              {viewButton("markdown", "Raw Markdown")}
            </span>
          </div>
          {view === "markdown" && (
            <pre>{markdown || "Markdown preview unavailable."}</pre>
          )}
          {view === "structured" && (
            <>
              <h4>Decisions ({asArray(document.decisions).length})</h4>
              <ContextItemList items={document.decisions} />
              <h4>Organization rules ({asArray(document.rules).length})</h4>
              <ContextItemList items={document.rules} />
              <h4>Repository events ({asArray(document.events).length})</h4>
              <ContextItemList items={document.events} />
            </>
          )}
        </div>
      )}
    </article>
  );
}

function ProposalQueue({ proposals, repository, pending, onReview }) {
  const queue = proposals.filter((proposal) => proposal.repository === repository);
  return (
    <section className="panel promotions-panel">
      <div className="section-heading"><div><p className="eyebrow">REPOSITORY REVIEW</p><h2>Temporary decision queue</h2></div><span className="count-pill">{queue.length}</span></div>
      <p className="panel-caption">A temporary decision is visible only in its source repository while pending. Approval creates a repository decision; it still is not organization consensus.</p>
      {queue.length
        ? <div className="promotion-list">{queue.map((proposal) => <ProposalReviewCard key={proposal.uid} proposal={proposal} pending={Boolean(pending[`proposal-${proposal.uid}`])} onReview={onReview} />)}</div>
        : <p className="empty-copy">No temporary decisions await repository review.</p>}
    </section>
  );
}

function RepositoryDetail({ repository, decisions, events, records, pending, onPromoteDecision, onDeleteDecision }) {
  const repoDecisions = decisions.filter((view) => view.repository === repository.name);
  const repoEvents = events.filter((view) => view.repository === repository.name);
  const repoRecords = records.filter((record) => record.repository === repository.name);
  const href = repositoryHref(repository.repository_url);

  return (
    <section className="panel decisions-panel repo-detail">
      <div className="section-heading">
        <div>
          <p className="eyebrow">REPOSITORY</p>
          <h2>{repository.name}</h2>
          {href && <a className="repository-link" href={href} target="_blank" rel="noreferrer">{href} ↗</a>}
        </div>
        <span className="count-pill">{repoDecisions.length}</span>
      </div>
      <p className="panel-caption">
        {repository.source ? `Synced via ${repository.source} · ` : ""}Last sync {formatDate(repository.last_synced_at)}.
        Decisions stay repository-local unless the repository explicitly promotes one for organization-rule review.
      </p>
      <div className="decision-list">
        {repoDecisions.length
          ? sortDecisions(repoDecisions).map((view) => (
              <DecisionCard
                key={`${view.repository}-${view.decision.uid}`}
                view={view}
                onPromote={onPromoteDecision}
                onDelete={onDeleteDecision}
                actionPending={Boolean(pending[`promote-${view.decision.uid}`] || pending[`delete-${view.decision.uid}`])}
              />
            ))
          : <p className="empty-copy">No decisions have been synced for this repository yet.</p>}
      </div>
      <div className="section-heading event-heading">
        <div><p className="eyebrow">REPOSITORY</p><h2>Events / cases</h2></div>
        <span className="count-pill">{repoEvents.length}</span>
      </div>
      {repoEvents.length ? (
        <ul className="policy-list">
          {repoEvents.map(({ event }) => (
            <li key={event.uid}>
              <div className="policy-head"><span className="decision-id">{event.id}</span></div>
              <strong>{event.title}</strong><p>{event.statement}</p>
            </li>
          ))}
        </ul>
      ) : <p className="empty-copy">No events have been synced for this repository yet.</p>}

      <div className="section-heading event-heading">
        <div><p className="eyebrow">AUDIT</p><h2>Recent context records</h2></div>
        <span className="count-pill">{repoRecords.length}</span>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr><th>Record</th><th>Agent / role</th><th>Delivered</th><th>Hash</th><th>Received</th></tr>
          </thead>
          <tbody>
            {repoRecords.length ? repoRecords.map((record) => (
              <tr key={record.uid}>
                <td title={record.summary || undefined}>{record.id}</td>
                <td>{record.agent}{record.role ? ` / ${record.role}` : ""}</td>
                <td title={asArray(record.decision_ids).join(", ") || undefined}>
                  {asArray(record.decision_ids).length} decision{asArray(record.decision_ids).length === 1 ? "" : "s"}
                  {asArray(record.rule_ids).length ? ` · ${asArray(record.rule_ids).length} rule${asArray(record.rule_ids).length === 1 ? "" : "s"}` : ""}
                  {asArray(record.event_ids).length ? ` · ${asArray(record.event_ids).length} event${asArray(record.event_ids).length === 1 ? "" : "s"}` : ""}
                </td>
                <td className="mono" title={record.context_hash}>{shortHash(record.context_hash)}</td>
                <td>{formatDate(record.received_at)}</td>
              </tr>
            )) : (
              <tr><td className="empty-copy" colSpan="5">No context has been recorded for this repository yet. Use agc context --record after agc pull.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export default function App() {
  const [token, setToken] = useState(() => localStorage.getItem("agc-server-token") || "");
  const [tokenDraft, setTokenDraft] = useState(token);
  const [authRequired, setAuthRequired] = useState(false);
  // Explicit screen state machine: loading → auth | error | empty | ready.
  const [screen, setScreen] = useState("loading");
  const [screenError, setScreenError] = useState("");
  const [organizations, setOrganizations] = useState([]);
  const [selectedSlug, setSelectedSlug] = useState("");
  const [selectedRepo, setSelectedRepo] = useState("");
  const [detail, setDetail] = useState(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [notices, setNotices] = useState([]);
  const [pending, setPending] = useState({});
  const noticeId = useRef(0);
  const detailRequestId = useRef(0);

  const dismissNotice = useCallback((id) => {
    setNotices((current) => current.filter((notice) => notice.id !== id));
  }, []);

  const notify = useCallback((message, kind = "success") => {
    const id = ++noticeId.current;
    setNotices((current) => [...current, { id, message, kind }]);
    window.setTimeout(() => dismissNotice(id), 4200);
  }, [dismissNotice]);

  const request = useCallback(async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    if (options.body) headers.set("Content-Type", "application/json");
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const response = await fetch(`${API_ROOT}${path}`, { ...options, headers });
    const body = (response.headers.get("content-type") || "").includes("application/json")
      ? await response.json()
      : null;
    if (!response.ok) {
      const error = new Error(body?.error || `Request failed (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return body;
  }, [token]);

  const fetchDetail = useCallback(async (slug) => (
    normalizeDetail(await request(`/organizations/${encodeURIComponent(slug)}`))
  ), [request]);

  const bootstrap = useCallback(async (preferredSlug = "") => {
    setScreen("loading");
    try {
      const healthResponse = await fetch("/healthz");
      const health = (healthResponse.headers.get("content-type") || "").includes("application/json")
        ? await healthResponse.json()
        : null;
      if (!healthResponse.ok) throw new Error(health?.error || `Server check failed (${healthResponse.status})`);
      setAuthRequired(health?.auth_required === true);

      const list = asArray(await request("/organizations"));
      setOrganizations(list);
      if (!list.length) {
        setDetail(null);
        setSelectedSlug("");
        setSelectedRepo("");
        writeLocation("", "", true);
        setScreen("empty");
        return;
      }
      const location = readLocation();
      const selected = list.find((organization) => organization.slug === preferredSlug)
        || list.find((organization) => organization.slug === location.org)
        || list[0];
      const detailResponse = await fetchDetail(selected.slug);
      const initialRepo = detailResponse.repositories.some((repository) => repository.name === location.repo)
        ? location.repo
        : "";
      setDetail(detailResponse);
      setSelectedSlug(detailResponse.organization.slug);
      setSelectedRepo(initialRepo);
      writeLocation(detailResponse.organization.slug, initialRepo, true);
      setScreen("ready");
    } catch (error) {
      if (error.status === 401) {
        setAuthRequired(true);
        setScreen("auth");
      } else {
        setScreenError(error.message);
        setScreen("error");
      }
    }
  }, [fetchDetail, request]);

  useEffect(() => { bootstrap(); }, [bootstrap]);

  async function runAction(key, action) {
    setPending((current) => ({ ...current, [key]: true }));
    try {
      await action();
    } finally {
      setPending((current) => ({ ...current, [key]: false }));
    }
  }

  async function selectOrganization(slug) {
    if (slug === selectedSlug || detailLoading) return;
    const requestId = ++detailRequestId.current;
    setSelectedSlug(slug);
    setSelectedRepo("");
    writeLocation(slug, "");
    setDetailLoading(true);
    try {
      const detailResponse = await fetchDetail(slug);
      if (requestId === detailRequestId.current) setDetail(detailResponse);
    } catch (error) {
      notify(error.message, "error");
    } finally {
      if (requestId === detailRequestId.current) setDetailLoading(false);
    }
  }

  function openRepository(name) {
    setSelectedRepo(name);
    writeLocation(selectedSlug, name);
  }

  // Browser back/forward replays the URL into the selection state.
  useEffect(() => {
    function onPopState() {
      const location = readLocation();
      if (!location.org || location.org === selectedSlug) {
        setSelectedRepo(location.repo);
        return;
      }
      const requestId = ++detailRequestId.current;
      setSelectedSlug(location.org);
      setSelectedRepo("");
      setDetailLoading(true);
      fetchDetail(location.org)
        .then((detailResponse) => {
          if (requestId !== detailRequestId.current) return;
          setDetail(detailResponse);
          setSelectedRepo(detailResponse.repositories.some((repository) => repository.name === location.repo) ? location.repo : "");
        })
        .catch((error) => notify(error.message, "error"))
        .finally(() => { if (requestId === detailRequestId.current) setDetailLoading(false); });
    }
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, [selectedSlug, fetchDetail, notify]);

  function connect() {
    const nextToken = tokenDraft.trim();
    localStorage.setItem("agc-server-token", nextToken);
    if (nextToken === token) {
      bootstrap();
    } else {
      // Changing the token rebuilds `request`, which re-runs the bootstrap effect.
      setToken(nextToken);
    }
  }

  async function createOrganization(name) {
    await runAction("createOrganization", async () => {
      try {
        const organization = await request("/organizations", {
          method: "POST",
          body: JSON.stringify({ name })
        });
        notify(`${organization.name} created`);
        await bootstrap(organization.slug);
      } catch (error) {
        notify(error.message, "error");
      }
    });
  }

  async function reviewPromotion(promotion, payload) {
    await runAction(`review-${promotion.uid}`, async () => {
      try {
        const reviewed = await request(`/organizations/${encodeURIComponent(selectedSlug)}/promotions/${encodeURIComponent(promotion.uid)}`, {
          method: "PATCH",
          body: JSON.stringify(payload)
        });
        notify(payload.status === "approved"
          ? `${reviewed.id} approved as ${reviewed.rule_id}`
          : `${reviewed.id} rejected`);
        setDetail(await fetchDetail(selectedSlug));
      } catch (error) {
        notify(error.message, "error");
      }
    });
  }

  async function promoteDecision(view) {
    const { decision, repository } = view;
    await runAction(`promote-${decision.uid}`, async () => {
      try {
        const now = new Date().toISOString();
        await request(`/organizations/${encodeURIComponent(selectedSlug)}/promotions`, {
          method: "POST",
          body: JSON.stringify({
            repository,
            source: "dashboard",
            promotion: {
              schema_version: 1,
              id: decision.id,
              uid: crypto.randomUUID(),
              decision_id: decision.id,
              decision_uid: decision.uid,
              decision_hash: decision.content_hash,
              status: "submitted",
              created_at: now,
              updated_at: now
            }
          })
        });
        notify(`${decision.id} submitted for organization review`);
        setDetail(await fetchDetail(selectedSlug));
      } catch (error) {
        notify(error.message, "error");
      }
    });
  }

  async function deleteDecision(view) {
    const { decision, repository } = view;
    if (!window.confirm(`Delete ${decision.id} from ${repository}? This cannot be undone.`)) return;
    await runAction(`delete-${decision.uid}`, async () => {
      try {
        await request(
          `/organizations/${encodeURIComponent(selectedSlug)}/repositories/${encodeURIComponent(repository)}/decisions/${encodeURIComponent(decision.uid)}`,
          { method: "DELETE" }
        );
        notify(`${decision.id} deleted from ${repository}`);
        setDetail(await fetchDetail(selectedSlug));
      } catch (error) {
        notify(error.message, "error");
      }
    });
  }

  async function reviewProposal(proposal, payload) {
    await runAction(`proposal-${proposal.uid}`, async () => {
      try {
        const reviewed = await request(`/organizations/${encodeURIComponent(selectedSlug)}/proposals/${encodeURIComponent(proposal.uid)}`, { method: "PATCH", body: JSON.stringify(payload) });
        notify(payload.status === "approved" ? `${reviewed.id} approved as ${reviewed.decision_id}` : `${reviewed.id} rejected`);
        setDetail(await fetchDetail(selectedSlug));
      } catch (error) { notify(error.message, "error"); }
    });
  }

  async function previewContext(repository, role, agent) {
    try {
      return await request(
        `/organizations/${encodeURIComponent(selectedSlug)}/context?repository=${encodeURIComponent(repository)}&role=${encodeURIComponent(role)}&agent=${encodeURIComponent(agent)}`
      );
    } catch (error) {
      notify(error.message, "error");
      return null;
    }
  }

  async function previewContextMarkdown(repository, role, agent) {
    const headers = new Headers();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const path = `/organizations/${encodeURIComponent(selectedSlug)}/context?repository=${encodeURIComponent(repository)}&role=${encodeURIComponent(role)}&agent=${encodeURIComponent(agent)}&format=markdown`;
    try {
      const response = await fetch(`${API_ROOT}${path}`, { headers });
      if (!response.ok) throw new Error(`Request failed (${response.status})`);
      return await response.text();
    } catch (error) {
      notify(error.message, "error");
      return null;
    }
  }

  const summary = detail?.organization;
  const pendingProposals = asArray(detail?.proposals).filter((proposal) => proposal.status === "pending");
  const pendingPromotions = asArray(detail?.promotions).filter((view) => view.promotion?.status === "pending");
  const activeRepository = selectedRepo && detail
    ? detail.repositories.find((repository) => repository.name === selectedRepo) || null
    : null;

  return (
    <>
      <header className="topbar">
        <a className="brand" href="/" aria-label="Agent Consensus home">
          <span className="brand-mark">agc</span><span>Consensus Console</span>
        </a>
        <div className="connection">
          {authRequired && screen === "ready" && (
            <>
              <label className="token-field" htmlFor="access-token">
                Access token
                <input
                  id="access-token"
                  type="password"
                  autoComplete="off"
                  value={tokenDraft}
                  onChange={(event) => setTokenDraft(event.target.value)}
                  placeholder="required"
                />
              </label>
              <button className="button button-quiet" onClick={connect} type="button">Connect</button>
            </>
          )}
          <span className={`connection-status ${screen === "error" || screen === "auth" ? "error" : ""}`}>
            {screen === "loading" && "Connecting…"}
            {screen === "ready" && "Connected"}
            {screen === "auth" && "Access token required"}
            {screen === "error" && "Connection problem"}
            {screen === "empty" && "Connected"}
          </span>
        </div>
      </header>

      <main className="shell">
        {screen === "loading" && (
          <Screen eyebrow="LOADING" title="Contacting the agc server…">
            <p className="muted">Reading organizations and consensus state.</p>
          </Screen>
        )}

        {screen === "auth" && (
          <Screen eyebrow="AUTHENTICATION" title="This server requires an access token.">
            <p className="muted">Enter the token configured with AGC_SERVER_TOKEN when the server was started.</p>
            <form className="inline-form" onSubmit={(event) => { event.preventDefault(); connect(); }}>
              <label htmlFor="auth-token">Access token
                <input
                  id="auth-token"
                  type="password"
                  autoComplete="off"
                  autoFocus
                  value={tokenDraft}
                  onChange={(event) => setTokenDraft(event.target.value)}
                  placeholder="required"
                />
              </label>
              <button className="button" type="submit">Connect</button>
            </form>
          </Screen>
        )}

        {screen === "error" && (
          <Screen eyebrow="CONNECTION" title="Cannot reach the agc server.">
            <p className="muted">{screenError}</p>
            <button className="button" type="button" onClick={() => bootstrap(selectedSlug)}>Retry</button>
          </Screen>
        )}

        {screen === "empty" && (
          <Screen eyebrow="FIRST ORGANIZATION" title="Start the shared consensus graph.">
            <p className="muted">Organization rules, repository decisions and case records are collected here.</p>
            <form className="inline-form" onSubmit={(event) => {
              event.preventDefault();
              const form = event.currentTarget;
              const name = String(new FormData(form).get("name") || "").trim();
              createOrganization(name).then(() => form.reset());
            }}>
              <label htmlFor="organization-name">Organization name
                <input id="organization-name" name="name" required maxLength="80" placeholder="payment-team" />
              </label>
              <button className="button" disabled={pending.createOrganization} type="submit">
                {pending.createOrganization ? "Creating…" : "Create organization"}
              </button>
            </form>
          </Screen>
        )}

        {screen === "ready" && detail && (
          <>
            <section className="intro">
              <div>
                <p className="eyebrow">ORGANIZATION</p>
                <h1>{summary.name}</h1>
                <p className="lede">
                  {summary.decision_count} repository decision{summary.decision_count === 1 ? "" : "s"} · {summary.rule_count} organization rule{summary.rule_count === 1 ? "" : "s"} · updated {formatDate(summary.updated_at)}
                </p>
              </div>
              <label className="organization-picker" htmlFor="organization-select">
                Organization
                <select
                  id="organization-select"
                  value={selectedSlug}
                  onChange={(event) => selectOrganization(event.target.value)}
                  disabled={detailLoading}
                >
                  {organizations.map((organization) => (
                    <option value={organization.slug} key={organization.slug}>{organization.name}</option>
                  ))}
                </select>
              </label>
            </section>

            <section className="stats" aria-label="Organization summary">
              <Metric label="Repository decisions" value={summary.active_decision_count} detail={`${summary.decision_count} total`} />
              <Metric label="Pending decisions" value={summary.pending_proposal_count} detail="temporary local decisions awaiting review" />
              <Metric label="Pending promotions" value={summary.pending_promotion_count} detail="awaiting human approval" />
              <Metric label="Context records" value={summary.context_record_count} detail="agent context deliveries" />
              <Metric label="Repositories" value={summary.repository_count} detail="syncing into this org" />
              <Metric label="Organization rules" value={summary.rule_count} detail="shared consensus" />
            </section>

            <div className={`detail-region ${detailLoading ? "is-loading" : ""}`}>
              {activeRepository ? (
                <>
                <div className="repo-preview">
                  <ContextPreview
                    repository={activeRepository.name}
                    repositories={[]}
                    pending={pending.preview}
                    onPreview={async (repository, role, agent) => {
                      let result = null;
                      await runAction("preview", async () => { result = await previewContext(repository, role, agent); });
                      return result;
                    }}
                    onPreviewMarkdown={previewContextMarkdown}
                  />
                </div>
                <ProposalQueue
                  proposals={pendingProposals}
                  repository={activeRepository.name}
                  pending={pending}
                  onReview={reviewProposal}
                />
                <RepositoryDetail
                  repository={activeRepository}
                  decisions={detail.decisions}
                  events={detail.events}
                  records={detail.context_records}
                  pending={pending}
                  onPromoteDecision={promoteDecision}
                  onDeleteDecision={deleteDecision}
                />
                </>
              ) : (
              <>
              <section className="panel repositories-strip">
                <div className="section-heading">
                  <div><p className="eyebrow">SYNC SOURCES</p><h2>Repositories</h2></div>
                  <span className="count-pill">{summary.repository_count}</span>
                </div>
                {detail.repositories.length ? (
                  <div className="repository-grid">
                    {detail.repositories.map((repository) => (
                      <button
                        className="repository-card"
                        key={repository.name}
                        type="button"
                        onClick={() => openRepository(repository.name)}
                        title={`Open ${repository.name}`}
                      >
                        <div className="repository-name">{repository.name}</div>
                        <div className="repository-meta">{repository.decision_count} decisions · {repository.event_count} events</div>
                        {repository.source && <div className="repository-meta">via {repository.source}</div>}
                        <div className="repository-meta">Last sync {formatDate(repository.last_synced_at)}</div>
                      </button>
                    ))}
                  </div>
                ) : (
                  <p className="empty-copy">No repository has synced state yet. Run agc push from a repository.</p>
                )}
              </section>

              <section className="panel promotions-panel">
                <div className="section-heading">
                  <div><p className="eyebrow">HUMAN REVIEW</p><h2>Promotion queue</h2></div>
                  <span className="count-pill">{pendingPromotions.length}</span>
                </div>
                <p className="panel-caption">Decisions remain repository-local by default. Approving an explicit promotion creates a shared organization rule; the originating decision remains in its repository.</p>
                {pendingPromotions.length ? (
                  <div className="promotion-list">
                    {pendingPromotions.map((view) => (
                      <PromotionReviewCard
                        key={view.promotion.uid}
                        view={view}
                        pending={Boolean(pending[`review-${view.promotion.uid}`])}
                        onReview={reviewPromotion}
                      />
                    ))}
                  </div>
                ) : <p className="empty-copy">No promotions await review. A repository owner can request one with <code>agc decision promote D-…</code>.</p>}
              </section>

              <article className="panel policies-panel">
                <div className="section-heading">
                  <div><p className="eyebrow">ORGANIZATION</p><h2>Rules</h2></div>
                  <span className="count-pill">{summary.rule_count}</span>
                </div>
                {detail.rules.length ? (
                  <ul className="policy-list">
                    {detail.rules.map((policy) => (
                      <li key={policy.uid}>
                        <div className="policy-head">
                          <span className="decision-id">{policy.id}</span>
                          <span className={`status-badge status-${policy.status}`}>{policy.status}</span>
                        </div>
                        <strong>{policy.title}</strong>
                        <p>{policy.statement}</p>
                      </li>
                    ))}
                  </ul>
                ) : <p className="empty-copy">No organization rules yet. Approve a decision promotion to add shared consensus.</p>}
              </article>
              </>
              )}
            </div>
          </>
        )}
      </main>
      <NoticeStack notices={notices} onDismiss={dismissNotice} />
    </>
  );
}
