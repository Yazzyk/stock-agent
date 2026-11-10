export namespace contract {
	
	export class Policy {
	    networkAllowlist: string[];
	    pathRoots: string[];
	    allowExec: boolean;
	    maxReqPerMin: number;
	    maxToolCalls: number;
	    maxTokensPerRun: number;
	
	    static createFrom(source: any = {}) {
	        return new Policy(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.networkAllowlist = source["networkAllowlist"];
	        this.pathRoots = source["pathRoots"];
	        this.allowExec = source["allowExec"];
	        this.maxReqPerMin = source["maxReqPerMin"];
	        this.maxToolCalls = source["maxToolCalls"];
	        this.maxTokensPerRun = source["maxTokensPerRun"];
	    }
	}

}

export namespace kernel {
	
	export class MethodView {
	    name: string;
	    description?: string;
	
	    static createFrom(source: any = {}) {
	        return new MethodView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	    }
	}
	export class ToolView {
	    name: string;
	    title: string;
	    summary: string;
	    tags?: string[];
	    risk: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.title = source["title"];
	        this.summary = source["summary"];
	        this.tags = source["tags"];
	        this.risk = source["risk"];
	    }
	}
	export class PluginView {
	    id: string;
	    name: string;
	    version: string;
	    description: string;
	    author?: string;
	    dependsOn?: string[];
	    tags?: string[];
	    active: boolean;
	    skipped?: boolean;
	    error?: string;
	    tools: ToolView[];
	    ui: plugin.UISlot[];
	    methods: MethodView[];
	
	    static createFrom(source: any = {}) {
	        return new PluginView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.description = source["description"];
	        this.author = source["author"];
	        this.dependsOn = source["dependsOn"];
	        this.tags = source["tags"];
	        this.active = source["active"];
	        this.skipped = source["skipped"];
	        this.error = source["error"];
	        this.tools = this.convertValues(source["tools"], ToolView);
	        this.ui = this.convertValues(source["ui"], plugin.UISlot);
	        this.methods = this.convertValues(source["methods"], MethodView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class StatusView {
	    started: boolean;
	    plugins: number;
	    active: number;
	    tools: number;
	    ui: number;
	    services: string[];
	    policy: contract.Policy;
	    dataHealth?: Record<string, string>;
	    // Go type: time
	    time: any;
	
	    static createFrom(source: any = {}) {
	        return new StatusView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.started = source["started"];
	        this.plugins = source["plugins"];
	        this.active = source["active"];
	        this.tools = source["tools"];
	        this.ui = source["ui"];
	        this.services = source["services"];
	        this.policy = this.convertValues(source["policy"], contract.Policy);
	        this.dataHealth = source["dataHealth"];
	        this.time = this.convertValues(source["time"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class AuditRow {
	    time: string;
	    plugin: string;
	    kind: string;
	    action: string;
	    ok: boolean;
	    risk?: string;
	    elapsedMs?: number;
	    detail?: string;
	
	    static createFrom(source: any = {}) {
	        return new AuditRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.plugin = source["plugin"];
	        this.kind = source["kind"];
	        this.action = source["action"];
	        this.ok = source["ok"];
	        this.risk = source["risk"];
	        this.elapsedMs = source["elapsedMs"];
	        this.detail = source["detail"];
	    }
	}

}

export namespace plugin {
	
	export class UISlot {
	    id: string;
	    title: string;
	    icon?: string;
	    group?: string;
	    component: string;
	    order: number;
	    owner?: string;
	
	    static createFrom(source: any = {}) {
	        return new UISlot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.icon = source["icon"];
	        this.group = source["group"];
	        this.component = source["component"];
	        this.order = source["order"];
	        this.owner = source["owner"];
	    }
	}

}

