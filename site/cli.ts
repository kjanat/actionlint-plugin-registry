import { cli, command, flag } from '@kjanat/dreamcli';
import { buildSite } from '@/src/build.tsx';

export const build = command('build')
	.description('Build the static catalogue from exported registry JSON')
	.flag('input', flag.string().default('dist').describe('Exported registry JSON directory'))
	.flag('output', flag.string().default('dist').describe('Static site destination'))
	.action(async ({ flags, out }) => {
		const count = await buildSite(flags.input, flags.output);
		out.log(`Built ${count} plugin pages`);
	});

if (import.meta.main) await cli('registry-site').default(build).run();
