// Builds the andrewbaker.ninja post as WordPress block markup. A new post is saved as a DRAFT;
// an existing post keeps its status (a live post stays live).
//   node docs/blog/post.js            write docs/blog/post.html only
//   node docs/blog/post.js --upload   also upload images and create or update the draft
// Numbers come from docs/blog/numbers.json; a missing number prints as "measuring".
// --publish publishes it, which the owner asks for each time. Without it the status is left alone.
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..', '..');
const STATE = path.join(__dirname, 'wp-state.json'); // post id and uploaded media ids (git-ignored)
const N = JSON.parse(fs.readFileSync(path.join(__dirname, 'numbers.json'), 'utf8'));
const state = fs.existsSync(STATE) ? JSON.parse(fs.readFileSync(STATE, 'utf8')) : { media: {} };

// ---------- block helpers ----------
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const attrs = (o) => JSON.stringify(o)
  .replace(/--/g, '\\u002d\\u002d').replace(/</g, '\\u003c').replace(/>/g, '\\u003e')
  .replace(/&/g, '\\u0026').replace(/\\"/g, '\\u0022');
const blocks = [];
const p = (html) => blocks.push(`<!-- wp:paragraph -->\n<p>${html}</p>\n<!-- /wp:paragraph -->`);
const h2 = (t) => blocks.push(`<!-- wp:heading -->\n<h2 class="wp-block-heading">${t}</h2>\n<!-- /wp:heading -->`);
const h3 = (t) => blocks.push(`<!-- wp:heading {"level":3} -->\n<h3 class="wp-block-heading">${t}</h3>\n<!-- /wp:heading -->`);
const ul = (items) => blocks.push(`<!-- wp:list -->\n<ul class="wp-block-list">${items.map((i) =>
  `<!-- wp:list-item -->\n<li>${i}</li>\n<!-- /wp:list-item -->`).join('\n')}</ul>\n<!-- /wp:list -->`);
const code = (text) => blocks.push(`<!-- wp:cloudscale/code-block ${attrs({ content: text })} /-->`);
const hr = () => blocks.push('<!-- wp:separator -->\n<hr class="wp-block-separator has-alpha-channel-opacity"/>\n<!-- /wp:separator -->');
const table = (head, rows) => blocks.push(`<!-- wp:table -->\n<figure class="wp-block-table"><table class="has-fixed-layout"><thead><tr>${
  head.map((h) => `<th>${h}</th>`).join('')}</tr></thead><tbody>${
  rows.map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join('')}</tr>`).join('')}</tbody></table></figure>\n<!-- /wp:table -->`);
const used = new Set(); // every image the post uses, so --upload sends exactly these
const img = (file, caption) => {
  used.add(file);
  const m = state.media[file];
  if (!m) { blocks.push(`<!-- wp:paragraph -->\n<p><em>[image: ${esc(file)}: ${esc(caption)}]</em></p>\n<!-- /wp:paragraph -->`); return; }
  blocks.push(`<!-- wp:image {"id":${m.id},"sizeSlug":"large","linkDestination":"media"} -->\n<figure class="wp-block-image size-large"><a href="${m.url}"><img src="${m.large || m.url}" class="wp-image-${m.id}" alt="${esc(caption)}"/></a><figcaption class="wp-element-caption">${esc(caption)}</figcaption></figure>\n<!-- /wp:image -->`);
};
// A measured number, or "measuring" until the run that produces it has finished.
const n = (k, unit = '') => (N[k] === undefined || N[k] === null) ? '<em>measuring</em>' : `${N[k]}${unit}`;

const REPO = 'https://github.com/andrewbakercloudscale/colima-dashboard';
const INSTALL = 'curl -fsSL https://raw.githubusercontent.com/andrewbakercloudscale/colima-dashboard/main/install.sh | bash';

// ---------- the post ----------
const TITLE = 'Colima vs Rancher Desktop on a MacBook: Measured, and the Gaps Filled';

p(`I ran Rancher Desktop on my MacBook for a long time, and it did the job: Docker, Kubernetes and a window with buttons. Then I looked at what it was costing me. A 1.5 GB app, a Kubernetes cluster I never asked for starting every morning, a stop button that takes over half a minute, and, when I finally went looking, <strong>59 GB</strong> of old virtual machine disks sitting in my Library folder. So I replaced it with <a href="https://github.com/abiosoft/colima">Colima</a>, which is free, open source and far lighter.`);
p(`Colima has one real weakness: it is command line only. There is no window, no list of containers, no button for the things that go wrong. So I built one, <a href="${REPO}">Colima Dashboard</a>, and then measured both setups properly on the same Mac rather than trusting anyone's opinion, including mine. This post starts with a quick tour of the dashboard, then the numbers, the gaps you fall into when you switch, and how the dashboard fills them.`);
hr();

h2('1. Install It in One Paste');
p('Open Terminal, paste this, and press Return. It installs Colima and the Docker command line tools with Homebrew, downloads Colima Dashboard (checking its checksum), sets it to start when you log in, and opens it in your browser.');
code(INSTALL);
p(`Then press <strong>Start</strong> on the General page. The first start downloads a small Linux image and takes a minute or two; after that it is quick. Everything is on <a href="${REPO}">GitHub</a> under the Apache 2.0 licence.`);
hr();

h2('2. A Quick Tour: Your First Ten Minutes');
p('Before the numbers, here is what you actually get. Every screenshot below is the dashboard running on my Mac against a small demo shop: a web server, an API, Redis, MariaDB and a busy background worker in one Compose project, with three test suites, a load test and image builds running on a loop. Nothing is mocked up.');

h3('Step 1: Start Colima from the General page');
p('The first page shows your profile as a card: its state, CPUs, memory, disk, runtime and VM type, with Start, Stop and Restart. Underneath sits a short list of things to fix, and any updates for Colima or the dashboard. Press <strong>Start</strong> and the card goes green in about 11 seconds.');
p('The things to fix list is the point of the page: you should never have to go looking for trouble. It gathers failed checks, containers that ran out of memory, containers at their CPU or memory limit, a VM that is short of CPU or memory, and errors in your logs, each with a button that fixes it or shows exactly what to change. Container limits apply in one click after a confirmation; VM changes open Preferences already filled in, because they restart the VM.');
img('general.png', 'General: the profile at a glance, what needs fixing, and updates');

h3('Step 2: Run your stack and see it in Containers');
p('Run <code>docker compose up</code> as usual. Containers groups each Compose project together, shows ports as clickable links, and gives every container Start, Stop, Restart, Logs, Inspect, Shell, Files and Remove. The search box filters as you type.');
img('containers.png', 'Containers: Compose projects grouped, ports one click away');

h3('Step 3: Watch what the VM is doing on Monitor');
p('This is the page I open most. The top band answers "is my VM busy?" in one glance: CPU and memory gauges with plain English verdicts, averages over 1 minute, 15 minutes and an hour, what the VM costs your actual Mac in cores and gigabytes, network and disk rates, and how many containers are running.');
img('monitor-hero.png', 'Monitor: CPU and memory gauges, 1, 15 and 60 minute averages, and what it costs your Mac');
p('Below it, every graph splits the load by container, so the noisy one is obvious. The donut is right now; the stacked charts are the last 5 minutes to an hour. Test runs started with <code>docker compose run</code> are folded into their service rather than cluttering the legend with a new name per run. Hover any chart for exact values.');
img('monitor-containers.png', 'Who is using the CPU and memory: right now, and over the last 15 minutes, by container');
img('monitor-io.png', 'Network, disk I/O, and the Mac CPU and memory the VM really uses, as Activity Monitor counts it');
p('Two cards sit above the graphs. <strong>Recommendations</strong> turns what Monitor sees into changes with real numbers: "the kernel stopped this container at its 64 MiB limit, give it 128 MiB", or "the VM\'s 2 CPUs averaged 100% for a minute, give it 4", with Apply buttons. <strong>Why is it slow?</strong> answers the question for the whole Mac, not just the VM: how busy the Mac is, how much of that is the kernel, how many new processes start each second, and the apps using the most CPU over the last minutes, with the Colima VM marked. When your laptop fans spin up, this tells you in one glance whether to blame Colima or Chrome.');
img('monitor-why.png', 'Recommendations with Apply buttons, and Why is it slow? for the whole Mac');

h3('Step 4: Drill into one container');
p('Inspect on any container opens a drawer with live sparklines for CPU, memory, network and disk, updated every 2 seconds, plus its running processes, its full configuration and its environment.');
img('live.png', 'Inspect: live stats and processes for one container');
p('<strong>Shell</strong> opens a real terminal inside the container, in the page, with colours and resizing. <strong>Files</strong> browses its file system: open a text file to read it, or download a file or a whole folder as a tar. Volumes can be browsed the same way, even when no container is using them.');
img('shell.png', 'A shell into a running container, in the browser');
img('files.png', 'Files: browse, read and download from inside a container');

h3('Step 5: Size, age and Cyber risk for every image');
p('Images shows each image with its size, a <strong>bloat</strong> grade (how much of it is caches, docs and build leftovers you could drop, layer by layer), when it was <strong>last used</strong> (Docker does not record this, so the dashboard works it out from the containers made from it and remembers it after they are gone), and which containers use it now, one click away. The <strong>Cyber risk</strong> column grades each image from A to F with Trivy, by its riskiest component: F means a critical vulnerability that already has a fix, so upgrading that one package removes it.');
img('images.png', 'Images: size, bloat, Cyber risk and when each image was last used');
p('Security lists every image with its grade, its riskiest components and how many of its vulnerabilities have a fix. Open one and a Components card shows each package with its grade and the version to upgrade to. Scans are kept until you press <strong>Recalculate Cyber Risk</strong>, so the page is instant and nothing scans behind your back.');
img('security.png', 'Security: a Cyber risk grade per image, its riskiest components and what to upgrade');

h3('Step 6: See how your tests are doing');
p('Every container that runs and exits is recorded, so test suites run in containers get a history for free, with nothing to install in your project. Runs and tests shows the pass rate, median and slowest 10% for each service, a trend bar per run, and flags for <strong>flaky</strong> tests (passing and failing on the same code), tests getting <strong>slower</strong>, and anything killed for running <strong>out of memory</strong>. In the demo, the integration suite fails about one run in four and is flagged flaky; the load test hits its 24 MB limit and is flagged out of memory.');
img('runs.png', 'Runs and tests: pass rates, timings, trends and flaky, slower and out of memory flags');

p('A test killed for memory gets a <strong>Fix</strong> button right on its row: it shows the limit it ran with, the memory it reached and a recommended limit, and for a Compose service, the line to put in your compose file.');

h3('Step 7: Keep an eye on your builds');
p('Builds reads BuildKit\'s own history: every image build, how long it took, how many steps came from cache, and, when one fails, the step and the error, without digging through terminal scrollback. Each image gets a trend, and a build that has become noticeably slower than its usual is flagged.');
img('builds.png', 'Builds: time, cache hits, trend and the exact error for a failed build');

h3('Step 8: Look back over days, not minutes');
p('<strong>Report</strong> turns that history into one page you can download as Markdown or save as a PDF: runs per day split into passed, failed and out of memory; the pass rate for tests and builds; errors per day; why things failed, with exit codes explained in plain English; test and build durations with slowdowns flagged; the jobs causing the most trouble; CPU, memory and disk trends; and an Out of date card for old images, <code>:latest</code> tags, dangling images and tools that are behind.');
img('report.png', 'Report: runs, pass rate, errors, speed changes, resources and what is out of date, over 1, 7 or 30 days');
p('Monitor also keeps 30 days of history on your Mac: the Mac CPU and memory the VM used, its disk on your Mac, Docker\'s disk use and the number of running containers, and, once it has a day of history, a forecast of when the VM disk fills up. Switch between 1 day, 7 days and 30 days at the top. It samples every 5 minutes while the dashboard runs, with the page open or not, and nothing leaves your Mac.');

h3('Step 9: Every error, from every container and pod, on one page');
p('The <strong>Errors</strong> page reads the logs of every container and every Kubernetes pod and keeps only the error and warning lines, newest first. It understands the usual formats: <code>level=error</code>, JSON <code>"level":"error"</code>, nginx <code>[error]</code>, Kubernetes <code>E1118</code> lines, Python tracebacks, panics and out of memory kills, and it ignores lines like "0 errors". A chart shows when they happened, a table shows who logged them, and one click narrows the list to one source or opens its full log. Pick 15 minutes, an hour, 6 hours or a day. The sidebar badge counts errors from the last 15 minutes, and General lists them under things to fix, so you see them without opening the page.');
img('errors.png', 'Errors: error and warning lines from every container and pod, when they happened and who logged them');

h3('Step 10: When something goes wrong, press Fix');
p('Diagnostics runs about 15 checks, from the Docker context and the credential helper to kubectl, stale Rancher Desktop files and company proxy certificates. A missing tool such as kubectl, buildx or Compose has an <strong>Install</strong> button that runs Homebrew for you and shows its progress. Every other problem has a Fix button that opens Terminal with the exact command, so you can see what it does before it runs. The sidebar badge counts open problems, and failing tests, builds and out of memory kills also arrive as macOS notifications and in the optional menu bar icon.');
img('diagnostics.png', 'Diagnostics: each problem found, with its fix one click away');

h3('Step 11: Kubernetes and snapshots, when you need them');
p('Kubernetes is off until you want it. Then one switch turns on k3s and a picker chooses the version. The Kubernetes page is a small console: pods with their requests, limits and live use; the usual configuration problems (no limits, out of memory, CPU throttled, pending, missing probes, <code>:latest</code> images), each with a Fix that shows now and recommended side by side; events and warnings; Deployments, StatefulSets and DaemonSets with scale, restart and roll back; YAML you can view, edit and apply with a dry run first; a shell into any pod; and port forwards to your Mac. Snapshots save a stopped profile in about a second as an APFS clone, so you can try something risky and roll back.');
img('kubernetes.png', 'Kubernetes: pods with live use, crash loops and out of memory kills flagged, each with a Fix');
img('k8s-pod.png', 'One pod: status, its containers\' requests and limits, logs of the last run, a shell and port forwards');
img('snapshots-bench.png', 'Snapshots: save and restore a whole profile in seconds');

h3('Step 12: Prove it with stress-ng');
p(`The repository has a set of <a href="${REPO}/tree/main/examples/stress-ng">stress-ng scenarios in containers</a> that each break something on purpose. Run <code>./run.sh</code> in that folder and open General: within a minute everything appears there by itself, with no searching.`);
ul([
  '<strong>cpu-hog</strong> runs every VM CPU at 100% for 5 minutes: "High CPU in the VM, 2 CPUs 100% busy on average over 60 seconds", with a button to set 4 CPUs.',
  '<strong>oom-test</strong> asks for 256 MB inside a 64 MB limit and is killed by the kernel: "ran out of memory, stopped at its 64 MiB memory limit", with Apply recommendation (128 MiB).',
  '<strong>perf-check</strong> is a performance test that fails its target and prints an ERROR line: it shows as a failed run and on the Errors page.',
  '<strong>noisy-logger</strong> logs an error and a warning every few seconds: "25 errors in logs, last 15 minutes", with Show errors.',
  '<strong>io-hog</strong> writes to disk hard: the Monitor disk chart names it.',
]);
img('stress-general.png', 'General after ./run.sh: every problem the stress tests caused, each with its fix');
hr();

h2('3. Why I Moved Off Rancher Desktop');
p('None of these is a bug. Each is a reasonable choice for a product that tries to be everything for everyone. Together they add up to a lot of machine for someone who mostly runs <code>docker compose up</code>.');
ul([
  `<strong>Size.</strong> The download is ${n('rd_download_mb', ' MB')} and the app is ${n('rd_app_gb', ' GB')} on disk, before it creates a single virtual machine. It bundles its own copies of Lima, the Docker CLI, kubectl, helm, nerdctl and more.`,
  `<strong>Kubernetes on by default.</strong> A fresh install starts a k3s cluster. On my Mac the first start took <strong>${n('rd_first_start_s', ' seconds')}</strong> before <code>docker run hello-world</code> worked, most of it Kubernetes.`,
  `<strong>A big virtual machine.</strong> On my 32 GB Mac it picked 2 CPUs and <strong>6 GB</strong> of memory for itself. Colima starts with 2 CPUs and 2 GB.`,
  `<strong>Slow to stop.</strong> Stopping took about <strong>${n('rd_stop_s', ' seconds')}</strong>, every single run.`,
  `<strong>It leaves things behind.</strong> Uninstalling the app left 59 GB of VM disks in <code>~/Library/Application Support/rancher-desktop</code>, a <code>~/.rd/bin</code> line in two of my shell profiles, and its settings, which quietly came back when I reinstalled it.`,
]);
hr();

h2('4. How I Measured');
p(`Same Mac, same day, one tool running at a time with the other fully stopped. The scripts and every raw CSV are in the repo under <a href="${REPO}/tree/main/bench"><code>bench/</code></a>, so you can run them on your own machine.`);
table(['', ''], [
  ['Mac', 'MacBook with Apple M5, 10 cores, 32 GB, macOS 27.2'],
  ['Rancher Desktop', '1.25.0, VZ virtual machine, Docker (moby) engine'],
  ['Colima', '0.10.3, VZ virtual machine, virtiofs mounts, Docker engine'],
  ['Timed tests', '5 runs each, median reported'],
  ['Idle', '5 minutes to settle, then a sample every 5 seconds for 10 minutes'],
]);
p('I tested three setups, because a comparison of defaults alone flatters whichever tool picks the smaller VM:');
ul([
  '<strong>Out of the box:</strong> each tool exactly as installed. This is what a new user gets.',
  '<strong>Matched:</strong> both at 4 CPUs and 8 GB, Kubernetes off. This shows the cost of the tool itself rather than the size of its VM.',
  '<strong>Matched with Kubernetes:</strong> as above with k3s on in both, since Kubernetes is Rancher Desktop\'s headline feature.',
]);
p('One honest caveat: this is my working Mac, with other work running in the background. I recorded the load average with every sample and reran anything that looked disturbed, but the numbers are real world rather than laboratory.');
hr();

h2('5. The Numbers');
p('Colima wins on everything that comes from being small: starting, stopping, sitting idle and taking up disk. On the work itself, builds and file access, the two are close enough that the background noise on my Mac decides it. Here is the full table, every figure a median of five runs or a ten minute idle average.');
table(['Measure', 'Rancher Desktop', 'Colima'], [
  ['Installed size', `${n('rd_app_gb', ' GB')} app (${n('rd_download_mb', ' MB')} download)`, `${n('co_installed_mb', ' MB')} (Colima, Lima, Docker CLI)`],
  ['First ever start, out of the box', n('rd_first_start_s', ' s') + ' (Kubernetes on)', n('co_first_start_s', ' s')],
  ['Cold start, out of the box', n('rd_cold_defaults_s', ' s'), n('co_cold_defaults_s', ' s')],
  ['Cold start, matched VMs', n('rd_cold_matched_s', ' s'), n('co_cold_matched_s', ' s')],
  ['Cold start to Kubernetes ready', n('rd_cold_k8s_s', ' s'), n('co_cold_k8s_s', ' s')],
  ['Stop, matched VMs', n('rd_stop_s', ' s'), n('co_stop_s', ' s')],
  ['Stop, with Kubernetes', n('rd_stop_k8s_s', ' s'), n('co_stop_k8s_s', ' s')],
  ['<code>docker run hello-world</code>, warm', n('rd_hello_s', ' s'), n('co_hello_s', ' s')],
  ['Idle memory, matched VMs', n('rd_idle_mem_matched_gb', ' GB'), n('co_idle_mem_matched_gb', ' GB')],
  ['Idle memory, with Kubernetes', n('rd_idle_mem_k8s_gb', ' GB'), n('co_idle_mem_k8s_gb', ' GB')],
  ['Idle CPU, matched VMs (one core = 100%)', n('rd_idle_cpu_matched', '%'), n('co_idle_cpu_matched', '%')],
  ['Idle CPU, with Kubernetes', n('rd_idle_cpu_k8s', '%'), n('co_idle_cpu_k8s', '%')],
  ['Image build, cold cache', n('rd_build_cold_s', ' s'), n('co_build_cold_s', ' s')],
  ['Bind mount file work (create, read, tar, delete)', n('rd_io_bind_s', ' s'), n('co_io_bind_s', ' s')],
  ['Left behind after uninstall', n('rd_leftovers'), '<code>~/.colima</code>, removed by <code>colima delete</code>'],
]);
img('chart-start.png', 'Cold start and stop times, median of 5 runs');
img('chart-idle.png', 'Memory and CPU used while idle, 10 minute average');
p('A few things stand out.');
ul([
  '<strong>Stopping is where the gap is widest.</strong> Rancher Desktop took 35 seconds to stop every single time, and 44 with Kubernetes. Colima took under 3. If you stop your VM at the end of the day, or to get battery back, you feel this every time.',
  '<strong>Kubernetes is expensive at idle in both, but much more so in Rancher Desktop.</strong> With nothing deployed, Rancher Desktop\'s k3s used 38% of a core and 5.4 GB on my Mac, all day. Colima\'s used 13% and 2.1 GB. Without Kubernetes, both are close to free.',
  '<strong>Builds and file access are a draw.</strong> Both run Docker in a Linux VM on Apple\'s Virtualization framework with virtiofs mounts, so the work inside runs at the same speed. One Rancher set of runs came out twice as slow, but my Mac was busy at the time (load average 14), so I have not counted it; the runs at similar load were within a few percent.',
  '<strong>The first start is the outlier.</strong> A fresh Rancher Desktop took over three minutes before <code>hello-world</code> worked, because it downloads and starts Kubernetes first. Colima took 40 seconds.',
]);
p('<strong>Verdict:</strong> for a developer on a Mac who wants Docker, and sometimes Kubernetes, Colima does the same work in about half the memory, starts twice as fast, stops ten times faster and leaves almost nothing behind. What it lacked was a window. That is the part I built.');
hr();

h2('6. What You Lose When You Leave Rancher Desktop');
p('Switching is mostly painless, but Rancher Desktop quietly did a few jobs that you only notice when they stop happening. I uninstalled it again while writing this, and every one of these happened:');
ul([
  '<strong>Docker stops working.</strong> Uninstalling removes <code>/var/run/docker.sock</code>, and Docker\'s current context can be left pointing at it. Every command fails until you run <code>docker context use colima</code>.',
  '<strong>kubectl, helm and buildx disappear.</strong> They lived in <code>~/.rd/bin</code>, which the uninstall empties. Install them from Homebrew. Without kubectl, Colima even reports Kubernetes as off and refuses to start a k3s profile.',
  '<strong>Image pulls fail with a confusing error.</strong> Rancher Desktop sets <code>"credsStore": "osxkeychain"</code> in <code>~/.docker/config.json</code> and ships the helper that goes with it. Remove the app and the setting can stay while the helper goes. The fix is <code>brew install docker-credential-helper</code>.',
  '<strong>Pulls fail behind a company proxy.</strong> Rancher Desktop copies your Mac\'s certificate authorities into its VM. Colima does not, so behind Zscaler or similar every pull fails with <code>x509: certificate signed by unknown authority</code>.',
  '<strong>Your PATH still points at Rancher.</strong> The <code>~/.rd/bin</code> lines stay in your shell profiles; on my Mac, three of them.',
  '<strong>Gigabytes stay on disk.</strong> One day of testing left 4.9 GB in <code>~/Library</code>. Months of real use had left 59 GB.',
  '<strong>No window.</strong> No list of containers, images or volumes, no Kubernetes switch, no logs a click away, nothing that tells you what is wrong.',
]);
hr();

h2('7. Colima Dashboard: Filling the Gaps');
p('Colima Dashboard is a small local web app for Colima. It is one Go binary with the page built in, it listens only on your own Mac, and it talks to Colima through its normal command line and to Docker through Colima\'s socket. There is no Electron and no Node. I went through Rancher Desktop page by page and setting by setting and copied every feature a Mac developer would miss, then added the things I always wanted from it. The tour in section 2 shows each page; this is the full list.');
h3('Everything Rancher Desktop had');
ul([
  '<strong>Containers, Images, Volumes, Port Forwarding:</strong> the everyday lists, with start, stop, logs, inspect and remove, Compose projects grouped together.',
  '<strong>A shell and a file browser</strong> for any running container, in the page. Volumes can be browsed too, even when no container uses them.',
  '<strong>Live stats</strong> per container: CPU, memory, network and disk I/O, and its processes.',
  '<strong>Images:</strong> pull, build from a folder on your Mac, push, and scan with Trivy, plus when each image was last used and which containers use it.',
  '<strong>Kubernetes:</strong> on or off, a k3s version picker, Traefik on or off, and a console: pods with live use and fixes, events, scale, restart and roll back, YAML editing with a dry run, pod shells and port forwards.',
  '<strong>Snapshots:</strong> save a stopped profile and roll back to it. They are APFS clones, so saving takes a second and uses almost no disk.',
  '<strong>A menu bar icon</strong> with each profile\'s state and start and stop, and notifications.',
  '<strong>Preferences:</strong> CPUs, memory, disk, runtime, VM type, mounts, Rosetta, profiles, start at login and a dark theme.',
]);
h3('Things Rancher Desktop never had');
ul([
  '<strong>Things to fix on the first page:</strong> out of memory kills, containers at their limits, a VM short of CPU or memory, and log errors, each with a recommendation in real numbers and a button to apply it.',
  '<strong>Summary on every page:</strong> totals, sizes, unused space, crashes, restarts, containers with no memory limit and ports open to your network, at the top of each page.',
  '<strong>Cyber risk:</strong> an A to F grade for every image and every component in it, cached until you press Recalculate Cyber Risk, with the version that fixes each one.',
  '<strong>Image bloat:</strong> a grade per image and the layers and files that make it big.',
  '<strong>Shells:</strong> every open shell into a container, pod or the VM on one page.',
  '<strong>Errors:</strong> error and warning lines from every container and pod in one list, with when and who.',
  '<strong>Report:</strong> runs, pass rate, errors, speed changes, resources and what is out of date over 1, 7 or 30 days, as Markdown or PDF.',
  '<strong>Why is it slow?</strong> What the whole Mac is busy with, with the Colima VM marked.',
  '<strong>Diagnostics with a fix button for every problem in section 6.</strong> Missing tools install with one click. For the rest, Terminal opens running the exact command, so you see what it does. Anything that deletes or edits a file asks first and keeps a backup. The company proxy one copies your Mac\'s certificates into the VM in one click.',
  '<strong>Runs and tests:</strong> every container that ran, how long it took and how it ended, with pass rates and the tests that are getting slower or flaky.',
  '<strong>Builds:</strong> build times, cache hit rates and why a build failed, over 30 days.',
  '<strong>Monitor:</strong> live graphs for the VM and each container, plus 30 days of history and a forecast of when the disk fills up.',
  '<strong>Alerts:</strong> a macOS notification when a test container or a build fails, or something runs out of memory.',
]);
p('It also keeps itself current: it checks GitHub for new releases of itself and of Colima, updates itself if you let it (checking the checksum first), and updates Colima through Homebrew with one click.');
hr();

h2('8. Moving Over: the Short Checklist');
p('If you would rather do it by hand, this is everything, in order. Colima Dashboard\'s Diagnostics page finds and fixes each of these for you.');
code([
  '# 1. Quit and remove Rancher Desktop',
  'osascript -e \'quit app "Rancher Desktop"\'',
  'brew uninstall --cask rancher',
  '',
  '# 2. Remove what it leaves behind (check the size first: mine was 59 GB)',
  'du -sh ~/Library/Application\\ Support/rancher-desktop',
  'rm -rf ~/Library/Application\\ Support/rancher-desktop ~/Library/Caches/rancher-desktop',
  'sed -i \'.bak\' \'/\\.rd\\/bin/d\' ~/.zshrc ~/.bash_profile ~/.bashrc',
  '',
  '# 3. Install Colima and the tools Rancher used to bundle',
  'brew install colima docker docker-compose docker-buildx docker-credential-helper kubectl',
  '',
  '# 4. Start it and point Docker at it',
  'colima start',
  'docker context use colima',
  'docker run --rm hello-world',
].join('\n'));
hr();

h2('9. When Rancher Desktop Is Still the Right Choice');
p('Colima is not for everyone, and I would not move a whole team over without checking these first. Stay on Rancher Desktop if you need Windows or Linux as well as macOS, Docker Desktop style extensions, or settings that your IT team locks down centrally. For one developer on a Mac who wants Docker and the occasional Kubernetes cluster, Colima with a small dashboard on top is the better trade.');
hr();

h2('10. Getting It');
p(`Colima Dashboard is free and open source under the Apache 2.0 licence: <a href="${REPO}">${REPO.replace('https://', '')}</a>. The one line install is in section 1, the tour in section 2, and the benchmark scripts are in the same repository. It is an independent project, not affiliated with Colima, Rancher Desktop or SUSE. If something does not work on your Mac, the dashboard has a Report a problem link on its first page.`);

const content = blocks.join('\n\n');
fs.writeFileSync(path.join(__dirname, 'post.html'), content);
console.log(`post.html: ${blocks.length} blocks, ${content.length} chars`);

// ---------- upload ----------
async function upload() {
  const { client } = require(path.join(ROOT, 'bench/tmp/wp/wp.js'));
  const { ctx, call } = await client();
  // charts live next to this file, screenshots in shots/
  const images = Object.fromEntries([...used].map((f) => [f, fs.existsSync(path.join(__dirname, f)) ? path.join(__dirname, f) : path.join(__dirname, 'shots', f)]));
  let changed = false;
  for (const [name, file] of Object.entries(images)) {
    if (!fs.existsSync(file)) continue;
    const sum = require('crypto').createHash('sha256').update(fs.readFileSync(file)).digest('hex').slice(0, 16);
    if (state.media[name]?.sum === sum) continue;
    // REST media uploads are refused by the site's edge gateway; the admin uploader is not.
    const nonce = (await (await ctx.get('/wp-admin/media-new.php')).text()).match(/"_wpnonce":"([0-9a-f]+)"/)?.[1];
    if (!nonce) throw new Error('no upload nonce from media-new.php');
    const fname = `colima-dashboard-${name}`;
    const res = await ctx.post('/wp-admin/async-upload.php', { multipart: {
      name: fname, action: 'upload-attachment', _wpnonce: nonce,
      'async-upload': { name: fname, mimeType: 'image/png', buffer: fs.readFileSync(file) } } });
    const up = await res.json().catch(() => ({}));
    if (!up.success) throw new Error(`upload ${name}: ${res.status()} ${JSON.stringify(up).slice(0, 200)}`);
    const m = { id: up.data.id, source_url: up.data.url, media_details: { sizes: { large: { source_url: up.data.sizes?.large?.url } } } };
    state.media[name] = { id: m.id, url: m.source_url, large: m.media_details?.sizes?.large?.source_url, sum };
    console.log('uploaded', name, m.id);
    changed = true;
  }
  fs.writeFileSync(STATE, JSON.stringify(state, null, 2));
  if (changed) return 'rerun'; // image blocks need the new ids: build again, then save
  // An update never sends a status unless --publish is given: sending 'draft' to the
  // live post unpublished it on 2026-11-19 (it vanished from the site until restored).
  const publish = process.argv.includes('--publish');
  const body = { title: TITLE, content: fs.readFileSync(path.join(__dirname, 'post.html'), 'utf8'), author: 2, categories: [17, 15] };
  if (publish) body.status = 'publish';
  else if (!state.postId) body.status = 'draft';
  const post = state.postId
    ? await call('POST', `/wp/v2/posts/${state.postId}`, { data: body })
    : await call('POST', '/wp/v2/posts', { data: body });
  if (publish && post.status !== 'publish') throw new Error('post is ' + post.status + ', expected publish');
  state.postId = post.id;
  fs.writeFileSync(STATE, JSON.stringify(state, null, 2));
  console.log(post.status, post.id, post.link);
  await ctx.dispose();
  return 'done';
}

if (process.argv.includes('--upload')) {
  upload().then((r) => {
    if (r === 'rerun') {
      require('child_process').execFileSync(process.execPath, [__filename, ...process.argv.slice(2)], { stdio: 'inherit' });
    }
  }).catch((e) => { console.error(e.message); process.exit(1); });
}
