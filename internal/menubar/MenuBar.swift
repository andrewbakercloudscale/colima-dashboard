// Colima Dashboard's menu bar icon. colima-dashboard starts it and passes the port and the
// session token in the environment (CD_PORT, CD_TOKEN, CD_URL); it talks only to that local
// server, so it holds no logic of its own: it shows each profile's state, starts and stops
// them, lists recent alerts and opens the dashboard.
//
// Build: swiftc -O -target arm64-apple-macos12 MenuBar.swift -o menubar-arm64 (and x86_64), lipo.
import AppKit

struct Profile: Decodable { let name: String; let status: String; let kubernetes: Bool; let busy: String? }
struct AlertItem: Decodable { let time: String; let profile: String; let title: String; let body: String }
struct State: Decodable { let profiles: [Profile]; let alerts: [AlertItem]?; let unread: Int?; let error: String? }

final class App: NSObject, NSApplicationDelegate, NSMenuDelegate {
    let env = ProcessInfo.processInfo.environment
    lazy var port = env["CD_PORT"] ?? "7766"
    lazy var token = env["CD_TOKEN"] ?? ""
    lazy var openURL = env["CD_URL"] ?? "http://127.0.0.1:\(port)/"
    lazy var base = "http://127.0.0.1:\(port)"
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    var state: State?
    var failures = 0

    func applicationDidFinishLaunching(_ n: Notification) {
        item.button?.image = NSImage(systemSymbolName: "shippingbox", accessibilityDescription: "Colima Dashboard")
        item.button?.imagePosition = .imageLeading
        let menu = NSMenu()
        menu.delegate = self
        item.menu = menu
        refresh()
        Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in self?.refresh() }
    }

    func request(_ method: String, _ path: String, _ done: ((Data?) -> Void)? = nil) {
        guard let url = URL(string: base + path) else { return }
        var req = URLRequest(url: url, timeoutInterval: 20)
        req.httpMethod = method
        req.setValue("colima_dashboard_session=\(token)", forHTTPHeaderField: "Cookie")
        if method != "GET" { req.setValue(base, forHTTPHeaderField: "Origin") }
        URLSession.shared.dataTask(with: req) { data, resp, _ in
            let ok = (resp as? HTTPURLResponse).map { $0.statusCode < 300 } ?? false
            DispatchQueue.main.async { done?(ok ? data : nil) }
        }.resume()
    }

    func refresh() {
        request("GET", "/api/menubar") { [weak self] data in
            guard let self else { return }
            if let data, let s = try? JSONDecoder().decode(State.self, from: data) {
                self.state = s
                self.failures = 0
            } else {
                self.failures += 1
                // The dashboard is gone (quit or replaced by an update): so is the icon.
                if self.failures >= 3 { NSApp.terminate(nil) }
            }
            self.paint()
        }
    }

    func paint() {
        let running = state?.profiles.contains { $0.status == "Running" } ?? false
        let unread = state?.unread ?? 0
        let name = unread > 0 ? "shippingbox.circle.fill" : (running ? "shippingbox.fill" : "shippingbox")
        item.button?.image = NSImage(systemSymbolName: name, accessibilityDescription: "Colima Dashboard")
        item.button?.title = unread > 0 ? " \(unread)" : ""
        item.button?.toolTip = running ? "Colima: running" : "Colima: stopped"
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        let header = NSMenuItem(title: "Colima Dashboard", action: nil, keyEquivalent: "")
        header.isEnabled = false
        menu.addItem(header)
        if let err = state?.error { menu.addItem(disabled(err)) }
        for p in state?.profiles ?? [] {
            let busy = p.busy.map { " (\($0)…)" } ?? ""
            let dot = p.status == "Running" ? "●" : "○"
            let row = NSMenuItem(title: "\(dot) \(p.name): \(p.status.lowercased())\(p.kubernetes ? ", Kubernetes" : "")\(busy)", action: nil, keyEquivalent: "")
            let sub = NSMenu()
            if p.status == "Running" {
                sub.addItem(action("Stop", #selector(act(_:)), ["stop", p.name]))
                sub.addItem(action("Restart", #selector(act(_:)), ["restart", p.name]))
            } else {
                sub.addItem(action("Start", #selector(act(_:)), ["start", p.name]))
            }
            row.submenu = sub
            menu.addItem(row)
        }
        if state?.profiles.isEmpty ?? true { menu.addItem(disabled("No Colima profiles yet")) }
        if let alerts = state?.alerts, !alerts.isEmpty {
            menu.addItem(.separator())
            menu.addItem(disabled("Recent alerts"))
            for a in alerts { menu.addItem(action("\(a.title)", #selector(open), nil)) }
            request("POST", "/api/alerts/read") { [weak self] _ in self?.refresh() }
        }
        menu.addItem(.separator())
        menu.addItem(action("Open Colima Dashboard", #selector(open), nil, key: "o"))
        menu.addItem(action("Hide this icon", #selector(hide), nil))
    }

    func disabled(_ title: String) -> NSMenuItem {
        let i = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        i.isEnabled = false
        return i
    }

    func action(_ title: String, _ sel: Selector, _ obj: Any?, key: String = "") -> NSMenuItem {
        let i = NSMenuItem(title: title, action: sel, keyEquivalent: key)
        i.target = self
        i.representedObject = obj
        return i
    }

    @objc func act(_ sender: NSMenuItem) {
        guard let a = sender.representedObject as? [String], a.count == 2,
              let p = a[1].addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) else { return }
        request("POST", "/api/profiles/\(p)/actions/\(a[0])") { [weak self] _ in self?.refresh() }
    }

    @objc func open() {
        if let url = URL(string: openURL) { NSWorkspace.shared.open(url) }
    }

    @objc func hide() {
        // Turning the setting off makes colima-dashboard stop this process.
        var req = URLRequest(url: URL(string: base + "/api/alerts/settings")!)
        req.httpMethod = "POST"
        req.setValue("colima_dashboard_session=\(token)", forHTTPHeaderField: "Cookie")
        req.setValue(base, forHTTPHeaderField: "Origin")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        // Keep the notification setting as it is; only the icon goes.
        request("GET", "/api/alerts") { data in
            var notify: Any = true
            if let data, let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let s = obj["settings"] as? [String: Any], let n = s["notify"] { notify = n }
            req.httpBody = try? JSONSerialization.data(withJSONObject: ["notify": notify, "menuBar": false])
            URLSession.shared.dataTask(with: req) { _, _, _ in DispatchQueue.main.async { NSApp.terminate(nil) } }.resume()
        }
    }
}

let app = NSApplication.shared
let delegate = App()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
