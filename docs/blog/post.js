// Builds the andrewbaker.ninja post as WordPress block markup and saves it as a DRAFT.
//   node docs/blog/post.js            write docs/blog/post.html only
//   node docs/blog/post.js --upload   also upload images and create or update the draft
// Numbers come from docs/blog/numbers.json; a missing number prints as "measuring".
// It saves a draft unless --publish is given, which the owner asks for each time.
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
const img = (file, caption) => {
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
p(`Colima has one real weakness: it is command line only. There is no window, no list of containers, no button for the things that go wrong. So I built one, <a href="${REPO}">Colima Dashboard</a>, and then measured both setups properly on the same Mac rather than trusting anyone's opinion, including mine. This post has the numbers, the gaps you fall into when you switch, and the tool that fills them.`);
hr();

h2('1. Install It in One Paste');
p('Open Terminal, paste this, and press Return. It installs Colima and the Docker command line tools with Homebrew, downloads Colima Dashboard (checking its checksum), sets it to start when you log in, and opens it in your browser.');
code(INSTALL);
p(`Then press <strong>Start</strong> on the General page. The first start downloads a small Linux image and takes a minute or two; after that it is quick. Everything is on <a href="${REPO}">GitHub</a> under the Apache 2.0 licence.`);
hr();

h2('2. Why I Moved Off Rancher Desktop');
p('None of these is a bug. Each is a reasonable choice for a product that tries to be everything for everyone. Together they add up to a lot of machine for someone who mostly runs <code>docker compose up</code>.');
ul([
  `<strong>Size.</strong> The download is ${n('rd_download_mb', ' MB')} and the app is ${n('rd_app_gb', ' GB')} on disk, before it creates a single virtual machine. It bundles its own copies of Lima, the Docker CLI, kubectl, helm, nerdctl and more.`,
  `<strong>Kubernetes on by default.</strong> A fresh install starts a k3s cluster. On my Mac the first start took <strong>${n('rd_first_start_s', ' seconds')}</strong> before <code>docker run hello-world</code> worked, most of it Kubernetes.`,
  `<strong>A big virtual machine.</strong> On my 32 GB Mac it picked 2 CPUs and <strong>6 GB</strong> of memory for itself. Colima starts with 2 CPUs and 2 GB.`,
  `<strong>Slow to stop.</strong> Stopping took about <strong>${n('rd_stop_s', ' seconds')}</strong>, every single run.`,
  `<strong>It leaves things behind.</strong> Uninstalling the app left 59 GB of VM disks in <code>~/Library/Application Support/rancher-desktop</code>, a <code>~/.rd/bin</code> line in two of my shell profiles, and its settings, which quietly came back when I reinstalled it.`,
]);
hr();

h2('3. How I Measured');
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

h2('4. The Numbers');
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

h2('5. What You Lose When You Leave Rancher Desktop');
p('Switching is mostly painless, but Rancher Desktop quietly did a few jobs that you only notice when they stop happening. I uninstalled it again while writing this, and every one of these happened:');
ul([
  '<strong>Docker stops working.</strong> Uninstalling removes <code>/var/run/docker.sock</code>, and Docker\'s current context can be left pointing at it. Every command fails until you run <code>docker context use colima</code>.',
  '<strong>kubectl, helm and buildx disappear.</strong> They lived in <code>~/.rd/bin</code>, which the uninstall empties. Install them from Homebrew.',
  '<strong>Image pulls fail with a confusing error.</strong> Rancher Desktop sets <code>"credsStore": "osxkeychain"</code> in <code>~/.docker/config.json</code> and ships the helper that goes with it. Remove the app and the setting can stay while the helper goes. The fix is <code>brew install docker-credential-helper</code>.',
  '<strong>Pulls fail behind a company proxy.</strong> Rancher Desktop copies your Mac\'s certificate authorities into its VM. Colima does not, so behind Zscaler or similar every pull fails with <code>x509: certificate signed by unknown authority</code>.',
  '<strong>Your PATH still points at Rancher.</strong> The <code>~/.rd/bin</code> lines stay in your shell profiles; on my Mac, three of them.',
  '<strong>Gigabytes stay on disk.</strong> One day of testing left 4.9 GB in <code>~/Library</code>. Months of real use had left 59 GB.',
  '<strong>No window.</strong> No list of containers, images or volumes, no Kubernetes switch, no logs a click away, nothing that tells you what is wrong.',
]);
hr();

h2('6. Colima Dashboard: Filling the Gaps');
p('Colima Dashboard is a small local web app for Colima. It is one Go binary with the page built in, it listens only on your own Mac, and it talks to Colima through its normal command line and to Docker through Colima\'s socket. There is no Electron and no Node. I went through Rancher Desktop page by page and setting by setting and copied every feature a Mac developer would miss, then added the things I always wanted from it.');
img('general.png', 'General: the profile, its state, what needs fixing, and updates');
h3('Everything Rancher Desktop had');
ul([
  '<strong>Containers, Images, Volumes, Port Forwarding:</strong> the everyday lists, with start, stop, logs, inspect and remove, Compose projects grouped together.',
  '<strong>A shell and a file browser</strong> for any running container, in the page. Volumes can be browsed too, even when no container uses them.',
  '<strong>Live stats</strong> per container: CPU, memory, network and disk I/O, and its processes.',
  '<strong>Images:</strong> pull, build from a folder on your Mac, push, and scan with Trivy.',
  '<strong>Kubernetes:</strong> on or off, a k3s version picker, Traefik on or off, workloads with their logs, and reset.',
  '<strong>Snapshots:</strong> save a stopped profile and roll back to it. They are APFS clones, so saving takes a second and uses almost no disk.',
  '<strong>A menu bar icon</strong> with each profile\'s state and start and stop, and notifications.',
  '<strong>Preferences:</strong> CPUs, memory, disk, runtime, VM type, mounts, Rosetta, profiles, start at login and a dark theme.',
]);
img('shell.png', 'A shell into a running container, in the browser');
img('kubernetes.png', 'Kubernetes: version, Traefik, workloads and reset');
h3('Things Rancher Desktop never had');
ul([
  '<strong>Diagnostics with a fix button for every problem in section 5.</strong> Press it and Terminal opens running the exact command, so you see what it does. Anything that deletes or edits a file asks first and keeps a backup. The company proxy one copies your Mac\'s certificates into the VM in one click.',
  '<strong>Runs and tests:</strong> every container that ran, how long it took and how it ended, with pass rates and the tests that are getting slower or flaky.',
  '<strong>Builds:</strong> build times, cache hit rates and why a build failed, over 30 days.',
  '<strong>Monitor:</strong> live graphs for the VM and each container, plus 30 days of history and a forecast of when the disk fills up.',
  '<strong>Alerts:</strong> a macOS notification when a test container or a build fails, or something runs out of memory.',
]);
img('diagnostics.png', 'Diagnostics: each problem found, with its fix one click away');
img('monitor.png', 'Monitor: what the VM costs your Mac, live and over 30 days');
p('It also keeps itself current: it checks GitHub for new releases of itself and of Colima, updates itself if you let it (checking the checksum first), and updates Colima through Homebrew with one click.');
hr();

h2('7. Moving Over: the Short Checklist');
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

h2('8. When Rancher Desktop Is Still the Right Choice');
p('Colima is not for everyone, and I would not move a whole team over without checking these first. Stay on Rancher Desktop if you need Windows or Linux as well as macOS, Docker Desktop style extensions, or settings that your IT team locks down centrally. For one developer on a Mac who wants Docker and the occasional Kubernetes cluster, Colima with a small dashboard on top is the better trade.');
hr();

h2('9. Getting It');
p(`Colima Dashboard is free and open source under the Apache 2.0 licence: <a href="${REPO}">${REPO.replace('https://', '')}</a>. The one line install is in section 1, and the benchmark scripts are in the same repository. It is an independent project, not affiliated with Colima, Rancher Desktop or SUSE. If something does not work on your Mac, the dashboard has a Report a problem link on its first page.`);

const content = blocks.join('\n\n');
fs.writeFileSync(path.join(__dirname, 'post.html'), content);
console.log(`post.html: ${blocks.length} blocks, ${content.length} chars`);

// ---------- upload ----------
async function upload() {
  const { client } = require(path.join(ROOT, 'bench/tmp/wp/wp.js'));
  const { ctx, call } = await client();
  const images = {
    ...Object.fromEntries(['general', 'diagnostics', 'monitor', 'shell', 'kubernetes'].map((k) => [`${k}.png`, path.join(__dirname, 'shots', `${k}.png`)])),
    'chart-start.png': path.join(__dirname, 'chart-start.png'),
    'chart-idle.png': path.join(__dirname, 'chart-idle.png'),
  };
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
  const body = { title: TITLE, content: fs.readFileSync(path.join(__dirname, 'post.html'), 'utf8'), status: process.argv.includes('--publish') ? 'publish' : 'draft', author: 2, categories: [17, 15] };
  const post = state.postId
    ? await call('POST', `/wp/v2/posts/${state.postId}`, { data: body })
    : await call('POST', '/wp/v2/posts', { data: body });
  const want = process.argv.includes('--publish') ? 'publish' : 'draft';
  if (post.status !== want) throw new Error('post is ' + post.status + ', expected ' + want);
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
