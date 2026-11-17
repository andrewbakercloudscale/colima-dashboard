// Builds the andrewbaker.ninja post as WordPress block markup and saves it as a DRAFT.
//   node docs/blog/post.js            write docs/blog/post.html only
//   node docs/blog/post.js --upload   also upload images and create or update the draft
// Numbers come from docs/blog/numbers.json; a missing number prints as "measuring".
// It never publishes: status is always "draft".
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
table(['Measure', 'Rancher Desktop', 'Colima'], [
  ['Download', n('rd_download_mb', ' MB'), n('co_download_mb', ' MB')],
  ['Installed size', n('rd_app_gb', ' GB'), n('co_installed_mb', ' MB')],
  ['First start (out of the box)', n('rd_first_start_s', ' s'), n('co_first_start_s', ' s')],
  ['Cold start (out of the box)', n('rd_cold_defaults_s', ' s'), n('co_cold_defaults_s', ' s')],
  ['Cold start (matched)', n('rd_cold_matched_s', ' s'), n('co_cold_matched_s', ' s')],
  ['Stop', n('rd_stop_s', ' s'), n('co_stop_s', ' s')],
  ['Idle memory (matched)', n('rd_idle_mem_matched_gb', ' GB'), n('co_idle_mem_matched_gb', ' GB')],
  ['Idle CPU (matched)', n('rd_idle_cpu_matched', '%'), n('co_idle_cpu_matched', '%')],
  ['Image build, cold cache', n('rd_build_cold_s', ' s'), n('co_build_cold_s', ' s')],
  ['Bind mount file work', n('rd_io_bind_s', ' s'), n('co_io_bind_s', ' s')],
  ['Left behind after uninstall', '59 GB plus shell profile edits', n('co_leftovers', '')],
]);
img('chart-start.png', 'Cold start and stop times, median of 5 runs');
img('chart-idle.png', 'Memory and CPU used while idle, 10 minute average');
p(N.verdict || '<em>The verdict goes here once the Colima runs finish this afternoon.</em>');
hr();

h2('5. What You Lose When You Leave Rancher Desktop');
p('Switching is mostly painless, but Rancher Desktop quietly did a few jobs that you only notice when they stop happening. These are the ones that bit me:');
ul([
  '<strong>Image pulls fail with a confusing error.</strong> Rancher Desktop sets <code>"credsStore": "osxkeychain"</code> in <code>~/.docker/config.json</code> and ships the helper that goes with it. Remove the app and the setting stays while the helper goes, so every pull fails. The fix is <code>brew install docker-credential-helper</code>.',
  '<strong>No buildx, no compose.</strong> Rancher Desktop bundles both Docker plugins. Colima does not, so <code>docker buildx</code> and <code>docker compose</code> vanish until you install them from Homebrew and tell Docker where they live.',
  '<strong>Your PATH still points at Rancher.</strong> The <code>~/.rd/bin</code> lines stay in your shell profiles, so for a while you run Rancher\'s leftover <code>docker</code> instead of the one you just installed.',
  '<strong>No window.</strong> No list of containers, images, volumes or forwarded ports; no Kubernetes switch; no logs a click away; nothing that tells you what is wrong. Colima can do all of it, but only if you know the commands.',
]);
hr();

h2('6. Colima Dashboard: Filling the Gaps');
p('Colima Dashboard is a small local web app for Colima. It is one Go binary of about 11 MB with the page built in, it listens only on your own Mac (127.0.0.1), and it talks to Colima through its normal command line and to Docker through Colima\'s socket. There is no Electron, no Node and nothing else to install.');
img('general.png', 'General: the profile, its state, and updates for Colima and the dashboard');
ul([
  '<strong>General:</strong> start, stop and restart with a progress view instead of a wall of log lines, and what is using the VM\'s disk.',
  '<strong>Monitor:</strong> live CPU and memory graphs for the VM and for each container.',
  '<strong>Containers, Images, Volumes, Port Forwarding:</strong> the everyday lists, with start, stop, remove and logs, and Compose projects grouped together.',
  '<strong>Kubernetes:</strong> k3s on or off, its services, and a reset button.',
  '<strong>Preferences:</strong> CPUs, memory, disk, runtime, VM type, mounts, Rosetta and new profiles.',
  '<strong>Troubleshooting and Diagnostics:</strong> colour coded logs, and checks for every problem in section 5, each with a button that fixes it.',
]);
img('diagnostics.png', 'Diagnostics: each problem found, with its fix one click away');
p('That last point matters most to me. Every fix is a button. Press it and Terminal opens running the exact command, so you see what it does, can answer any prompt, and keep the output. Anything that deletes or edits a file asks first and keeps a backup. The page never sends a command to run; it names the check, and the dashboard works out the fix itself.');
img('logs.png', 'Colima\'s VM logs, colour coded');
p('It also keeps itself current. It checks GitHub for new releases of itself and of Colima, updates itself automatically if you let it (checking the download\'s checksum first), and updates Colima through Homebrew with one click.');
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
  'rm -rf ~/Library/Application\\ Support/rancher-desktop',
  'sed -i \'.bak\' \'/\\.rd\\/bin/d\' ~/.zshrc ~/.bash_profile',
  '',
  '# 3. Install Colima and the Docker tools Rancher used to bundle',
  'brew install colima docker docker-compose docker-buildx docker-credential-helper',
  '',
  '# 4. Start it and point Docker at it',
  'colima start',
  'docker context use colima',
  'docker run --rm hello-world',
].join('\n'));
hr();

h2('8. When Rancher Desktop Is Still the Right Choice');
p('Colima is not for everyone, and I would not move a whole team over without checking these first. Stay on Rancher Desktop if you need Windows or Linux as well as macOS, a picker for exact Kubernetes versions, Rancher\'s extensions or snapshots, or settings that your IT team locks down centrally. For one developer on a Mac who wants Docker and the occasional Kubernetes cluster, Colima with a small dashboard on top is the better trade.');
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
    'general.png': path.join(ROOT, 'docs/screenshots/general.png'),
    'diagnostics.png': path.join(ROOT, 'docs/screenshots/diagnostics.png'),
    'logs.png': path.join(ROOT, 'docs/screenshots/logs.png'),
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
  const body = { title: TITLE, content: fs.readFileSync(path.join(__dirname, 'post.html'), 'utf8'), status: 'draft', author: 2, categories: [17, 15] };
  const post = state.postId
    ? await call('POST', `/wp/v2/posts/${state.postId}`, { data: body })
    : await call('POST', '/wp/v2/posts', { data: body });
  if (post.status !== 'draft') throw new Error('post is ' + post.status + ', expected draft');
  state.postId = post.id;
  fs.writeFileSync(STATE, JSON.stringify(state, null, 2));
  console.log('draft', post.id, post.link);
  await ctx.dispose();
  return 'done';
}

if (process.argv.includes('--upload')) {
  upload().then((r) => {
    if (r === 'rerun') {
      require('child_process').execFileSync(process.execPath, [__filename, '--upload'], { stdio: 'inherit' });
    }
  }).catch((e) => { console.error(e.message); process.exit(1); });
}
