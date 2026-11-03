import { useState } from "react";
import { Button, Icon, Notice } from "@panasms/ui";
import { translator } from "@panasms/i18n";
import { mdiPlus, mdiDeleteOutline } from "@mdi/js";
import {
  inherited,
  nodeAt,
  patchCompose,
  type Path,
  parseCompose,
} from "./compose-model";
import { isAlias } from "yaml";
const t = translator("containers");
type Model = ReturnType<typeof parseCompose>;
function AddName({
  label,
  add,
}: {
  label: string;
  add: (name: string) => boolean;
}) {
  const [value, set] = useState("");
  return (
    <div className="compose-line">
      <input
        aria-label={label}
        placeholder={label}
        value={value}
        onChange={(e) => set(e.target.value)}
      />
      <Button
        title={label}
        disabled={
          !/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(value) ||
          ["__proto__", "constructor", "prototype"].includes(value)
        }
        onClick={() => {
          if (add(value)) set("");
        }}
      >
        <Icon path={mdiPlus} />
      </Button>
    </div>
  );
}
export function Builder({
  text,
  parsed,
  change,
  code,
}: {
  text: string;
  parsed: Model;
  change: (s: string) => void;
  code: () => void;
}) {
  const services = parsed.model.services,
    names = Object.keys(services);
  const [selected, select] = useState(names[0] ?? ""),
    [error, report] = useState("");
  const name = names.includes(selected) ? selected : names[0],
    svc = services[name] ?? {},
    base = ["services", name];
  function set(path: Path, value: unknown, original = text) {
    try {
      const result = patchCompose(original, path, value);
      change(result);
      report("");
      return true;
    } catch (e) {
      report(t("composeEditYaml") + " " + (e as Error).message);
      return false;
    }
  }
  function add(path: Path, value: unknown) {
    if (nodeAt(parsed, path)) {
      report(t("composeExists"));
      return false;
    }
    return set(path, value);
  }
  function group(key: string, body: () => React.ReactNode) {
    const path = [...base, key],
      val = svc[key];
    return (
      <fieldset>
        <legend>{t("builder." + key)}</legend>
        {val !== undefined &&
        (inherited(parsed, path) || isAlias(nodeAt(parsed, path))) ? (
          <Button onClick={() => set(path, val)}>{t("composeOverride")}</Button>
        ) : (
          body()
        )}
      </fieldset>
    );
  }
  function field(
    label: string,
    value: unknown,
    write: (s: string) => void,
    secret = false,
  ) {
    return (
      <label>
        {t(label)}
        <input
          type={secret ? "password" : "text"}
          value={String(value ?? "")}
          onChange={(e) => write(e.target.value)}
        />
      </label>
    );
  }
  const advanced = <Button onClick={code}>{t("composeEditYaml")}</Button>;
  const remove = (path: Path) => (
    <Button title={t("remove")} onClick={() => set(path, undefined)}>
      <Icon path={mdiDeleteOutline} />
    </Button>
  );
  function mappings(key: "ports" | "volumes") {
    const rows = svc[key] ?? [],
      path = [...base, key];
    if (!Array.isArray(rows)) return advanced;
    const volumes = Object.keys(parsed.model.volumes ?? {});
    function addRow() {
      const source = volumes[0] ?? "data";
      const row =
        key === "ports"
          ? { target: 80, published: "8080" }
          : { type: "volume", source, target: "/data" };
      try {
        const draft =
          key === "volumes" && !volumes.length
            ? patchCompose(text, ["volumes", source], {})
            : text;
        set(
          svc[key] ? [...path, rows.length] : path,
          svc[key] ? row : [row],
          draft,
        );
      } catch (e) {
        report(t("composeEditYaml") + " " + (e as Error).message);
      }
    }
    return (
      <>
        {rows.map((row: any, i: number) => {
          const p = [...path, i];
          let value = row;
          if (key === "ports" && typeof row !== "object") {
            const match =
              /^(?:(?:(\[[^\]]+\]|[\d.]+):)?([\d]*):)?(\d+)(?:\/(tcp|udp))?$/.exec(
                String(row),
              );
            if (!match)
              return (
                <div key={i}>
                  {String(row)} {advanced}
                </div>
              );
            value = {
              ...(match[1] ? { host_ip: match[1] } : {}),
              ...(match[2] ? { published: match[2] } : {}),
              target: +match[3],
              protocol: match[4] || "tcp",
            };
          } else if (key === "volumes" && typeof row === "string") {
            const [source, target, mode, ...rest] = row.split(":");
            if (
              rest.length ||
              (mode && !["ro", "rw"].includes(mode)) ||
              !target
            )
              return (
                <div key={i}>
                  {row} {advanced}
                </div>
              );
            value = {
              type: /^[./]/.test(source) ? "bind" : "volume",
              source,
              target,
              ...(mode === "ro" ? { read_only: true } : {}),
            };
          }
          if (!value || typeof value !== "object" || isAlias(nodeAt(parsed, p)))
            return <div key={i}>{advanced}</div>;
          const update = (k: string, v: unknown) =>
            typeof row === "object"
              ? set([...p, k], v)
              : set(p, { ...value, [k]: v });
          return (
            <div className="compose-line" key={i}>
              {key === "ports" ? (
                <>
                  {field("hostPort", value.published, (v) =>
                    update("published", v),
                  )}
                  {field("containerPort", value.target, (v) =>
                    update("target", /^\d+$/.test(v) ? Number(v) : v),
                  )}
                  <label>
                    {t("protocol")}
                    <select
                      value={value.protocol ?? "tcp"}
                      onChange={(e) => update("protocol", e.target.value)}
                    >
                      {["tcp", "udp", "sctp"].map((x) => (
                        <option key={x}>{x}</option>
                      ))}
                    </select>
                  </label>
                </>
              ) : (
                <>
                  <label>
                    {t("composeMountType")}
                    <select
                      value={value.type}
                      onChange={(e) => update("type", e.target.value)}
                    >
                      {[...new Set([value.type, "bind", "volume"])].map((x) => (
                        <option key={x}>{x}</option>
                      ))}
                    </select>
                  </label>
                  {value.type === "volume" ? (
                    <label>
                      {t("composeSource")}
                      <select
                        value={value.source ?? ""}
                        onChange={(e) => update("source", e.target.value)}
                      >
                        {[...new Set([value.source ?? "", ...volumes])].map(
                          (n) => (
                            <option key={n}>{n}</option>
                          ),
                        )}
                      </select>
                    </label>
                  ) : (
                    field("composeSource", value.source, (v) =>
                      update("source", v),
                    )
                  )}
                  {field("containerPath", value.target, (v) =>
                    update("target", v),
                  )}
                  <label>
                    <input
                      type="checkbox"
                      checked={!!value.read_only}
                      onChange={(e) => update("read_only", e.target.checked)}
                    />
                    {t("composeReadOnly")}
                  </label>
                </>
              )}
              {remove(p)}
            </div>
          );
        })}
        <Button onClick={addRow}>
          {t(key === "ports" ? "addPort" : "addMount")}
        </Button>
      </>
    );
  }
  function choices(key: "networks" | "depends_on", choices: string[]) {
    const current = svc[key],
      array = Array.isArray(current),
      path = [...base, key];
    return (
      <div className="compose-choices">
        {choices.map((n) => {
          const index = array ? current.indexOf(n) : -1,
            checked = array
              ? index >= 0
              : !!current && Object.hasOwn(current, n);
          return (
            <label key={n}>
              <input
                type="checkbox"
                checked={checked}
                onChange={() => {
                  if (!current) set(path, [n]);
                  else if (array)
                    set(
                      [...path, checked ? index : current.length],
                      checked ? undefined : n,
                    );
                  else
                    set(
                      [...path, n],
                      checked
                        ? undefined
                        : key === "depends_on"
                          ? { condition: "service_started" }
                          : {},
                    );
                }}
              />
              {n}
            </label>
          );
        })}
      </div>
    );
  }
  return (
    <div className="containers-form compose-builder">
      {error && <Notice error>{error}</Notice>}
      <div className="compose-line">
        <label>
          {t("composeService")}
          <select
            value={name ?? ""}
            onChange={(e) => {
              select(e.target.value);
              report("");
            }}
          >
            {names.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
        </label>
        <AddName
          label={t("composeAddService")}
          add={(n) => {
            if (!add(["services", n], { image: "" })) return false;
            select(n);
            return true;
          }}
        />
        {name && remove(base)}
      </div>
      {name && typeof svc === "object" && (
        <>
          {group("image", () =>
            field("image", svc.image, (v) => set([...base, "image"], v)),
          )}
          {group("ports", () => mappings("ports"))}
          {group("volumes", () => mappings("volumes"))}
          {group("environment", () => {
            const env = svc.environment || {},
              path = [...base, "environment"];
            return (
              <>
                {Object.entries(env).map(([k, v]) => {
                  const pair = Array.isArray(env),
                    raw = String(v ?? ""),
                    index = raw.indexOf("="),
                    label = pair ? raw.split("=")[0] : k;
                  const p = [...path, pair ? Number(k) : k];
                  return (
                    <div className="compose-line" key={k}>
                      <label>
                        {label}
                        <input
                          aria-label={label}
                          type="password"
                          value={
                            pair
                              ? index < 0
                                ? ""
                                : raw.slice(index + 1)
                              : String(v ?? "")
                          }
                          onChange={(e) =>
                            set(
                              p,
                              pair
                                ? label + "=" + e.target.value
                                : e.target.value,
                            )
                          }
                        />
                      </label>
                      {remove(p)}
                    </div>
                  );
                })}
                <AddName
                  label={t("addVariable")}
                  add={(n) =>
                    Array.isArray(env)
                      ? set([...path, env.length], n + "=")
                      : add([...path, n], "")
                  }
                />
              </>
            );
          })}
          {group("restart", () =>
            field("restartPolicy", svc.restart, (v) =>
              set([...base, "restart"], v),
            ),
          )}
          <details>
            <summary>{t("composeAdvanced")}</summary>
            {group("depends_on", () =>
              choices(
                "depends_on",
                names.filter((n) => n !== name),
              ),
            )}
            {group("healthcheck", () => {
              const h = svc.healthcheck ?? {},
                test = h.test;
              return (
                <>
                  {!test || typeof test === "string" || test[0] === "CMD-SHELL"
                    ? field(
                        "composeCommand",
                        Array.isArray(test) ? test[1] : test,
                        (v) =>
                          set(
                            [...base, "healthcheck", "test"],
                            ["CMD-SHELL", v],
                          ),
                      )
                    : advanced}
                  {field("composeInterval", h.interval, (v) =>
                    set([...base, "healthcheck", "interval"], v),
                  )}
                </>
              );
            })}
            {group("networks", () =>
              choices("networks", Object.keys(parsed.model.networks ?? {})),
            )}
          </details>
        </>
      )}
      <details>
        <summary>{t("composeResources")}</summary>
        {(["networks", "volumes"] as const).map((key) => (
          <fieldset key={key}>
            <legend>{t(key)}</legend>
            {Object.keys(parsed.model[key] || {}).map((k) => (
              <span key={k}>{k} </span>
            ))}
            <AddName
              label={t(
                key === "networks" ? "composeAddNetwork" : "composeAddVolume",
              )}
              add={(n) => add([key, n], {})}
            />
          </fieldset>
        ))}
      </details>
    </div>
  );
}
