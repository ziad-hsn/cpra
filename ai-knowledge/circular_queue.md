<!DOCTYPE html>
<html class="client-nojs vector-feature-language-in-header-enabled vector-feature-language-in-main-page-header-disabled vector-feature-page-tools-pinned-disabled vector-feature-toc-pinned-clientpref-1 vector-feature-main-menu-pinned-disabled vector-feature-limited-width-clientpref-1 vector-feature-limited-width-content-enabled vector-feature-custom-font-size-clientpref-1 vector-feature-appearance-pinned-clientpref-1 vector-feature-night-mode-enabled skin-theme-clientpref-day vector-sticky-header-enabled vector-toc-available" lang="en" dir="ltr">
<head>
<meta charset="UTF-8">
<title>Circular buffer - Wikipedia</title>
<script>(function(){var className="client-js vector-feature-language-in-header-enabled vector-feature-language-in-main-page-header-disabled vector-feature-page-tools-pinned-disabled vector-feature-toc-pinned-clientpref-1 vector-feature-main-menu-pinned-disabled vector-feature-limited-width-clientpref-1 vector-feature-limited-width-content-enabled vector-feature-custom-font-size-clientpref-1 vector-feature-appearance-pinned-clientpref-1 vector-feature-night-mode-enabled skin-theme-clientpref-day vector-sticky-header-enabled vector-toc-available";var cookie=document.cookie.match(/(?:^|; )enwikimwclientpreferences=([^;]+)/);if(cookie){cookie[1].split('%2C').forEach(function(pref){className=className.replace(new RegExp('(^| )'+pref.replace(/-clientpref-\w+$|[^\w-]+/g,'')+'-clientpref-\\w+( |$)'),'$1'+pref+'$2');});}document.documentElement.className=className;}());RLCONF={"wgBreakFrames":false,"wgSeparatorTransformTable":["",""],"wgDigitTransformTable":["",""],"wgDefaultDateFormat":"dmy","wgMonthNames":["","January","February","March","April","May","June","July","August","September","October","November","December"],"wgRequestId":"fc0b0a0a-4624-4d32-822f-ff51edc093e6","wgCanonicalNamespace":"","wgCanonicalSpecialPageName":false,"wgNamespaceNumber":0,"wgPageName":"Circular_buffer","wgTitle":"Circular buffer","wgCurRevisionId":1284869780,"wgRevisionId":1284869780,"wgArticleId":11891734,"wgIsArticle":true,"wgIsRedirect":false,"wgAction":"view","wgUserName":null,"wgUserGroups":["*"],"wgCategories":["Articles with short description","Short description is different from Wikidata","All accuracy disputes","Articles with disputed statements from January 2022","Webarchive template wayback links","Computer memory","Arrays"],"wgPageViewLanguage":"en","wgPageContentLanguage":"en","wgPageContentModel":"wikitext","wgRelevantPageName":"Circular_buffer","wgRelevantArticleId":11891734,"wgIsProbablyEditable":true,"wgRelevantPageIsProbablyEditable":true,"wgRestrictionEdit":[],"wgRestrictionMove":[],"wgNoticeProject":"wikipedia","wgFlaggedRevsParams":{"tags":{"status":{"levels":1}}},"wgMediaViewerOnClick":true,"wgMediaViewerEnabledByDefault":true,"wgPopupsFlags":0,"wgVisualEditor":{"pageLanguageCode":"en","pageLanguageDir":"ltr","pageVariantFallbacks":"en"},"wgMFDisplayWikibaseDescriptions":{"search":true,"watchlist":true,"tagline":false,"nearby":true},"wgWMESchemaEditAttemptStepOversample":false,"wgWMEPageLength":10000,"wgMetricsPlatformUserExperiments":{"active_experiments":[],"overrides":[],"enrolled":[],"assigned":[],"subject_ids":[],"sampling_units":[]},"wgEditSubmitButtonLabelPublish":true,"wgULSPosition":"interlanguage","wgULSisCompactLinksEnabled":false,"wgVector2022LanguageInHeader":true,"wgULSisLanguageSelectorEmpty":false,"wgWikibaseItemId":"Q1224994","wgCheckUserClientHintsHeadersJsApi":["brands","architecture","bitness","fullVersionList","mobile","model","platform","platformVersion"],"GEHomepageSuggestedEditsEnableTopics":true,"wgGESuggestedEditsTaskTypes":{"taskTypes":["copyedit","link-recommendation"],"unavailableTaskTypes":[]},"wgGETopicsMatchModeEnabled":false,"wgGELevelingUpEnabledForUser":false};
RLSTATE={"ext.globalCssJs.user.styles":"ready","site.styles":"ready","user.styles":"ready","ext.globalCssJs.user":"ready","user":"ready","user.options":"loading","ext.cite.styles":"ready","ext.pygments":"ready","ext.wikimediamessages.styles":"ready","skins.vector.search.codex.styles":"ready","skins.vector.styles":"ready","skins.vector.icons":"ready","jquery.makeCollapsible.styles":"ready","ext.visualEditor.desktopArticleTarget.noscript":"ready","ext.uls.interlanguage":"ready","wikibase.client.init":"ready"};RLPAGEMODULES=["ext.xLab","ext.cite.ux-enhancements","ext.pygments.view","mediawiki.page.media","site","mediawiki.page.ready","jquery.makeCollapsible","mediawiki.toc","skins.vector.js","ext.centralNotice.geoIP","ext.centralNotice.startUp","ext.gadget.ReferenceTooltips","ext.gadget.switcher","ext.urlShortener.toolbar","ext.centralauth.centralautologin","mmv.bootstrap","ext.popups","ext.visualEditor.desktopArticleTarget.init","ext.visualEditor.targetLoader","ext.echo.centralauth","ext.eventLogging","ext.wikimediaEvents","ext.navigationTiming","ext.uls.interface","ext.cx.eventlogging.campaigns","ext.cx.uls.quick.actions","wikibase.client.vector-2022","ext.checkUser.clientHints","ext.quicksurveys.init","ext.growthExperiments.SuggestedEditSession"];</script>
<script>(RLQ=window.RLQ||[]).push(function(){mw.loader.impl(function(){return["user.options@12s5i",function($,jQuery,require,module){mw.user.tokens.set({"patrolToken":"+\\","watchToken":"+\\","csrfToken":"+\\"});
}];});});</script>
<link rel="stylesheet" href="/w/load.php?lang=en&amp;modules=ext.cite.styles%7Cext.pygments%7Cext.uls.interlanguage%7Cext.visualEditor.desktopArticleTarget.noscript%7Cext.wikimediamessages.styles%7Cjquery.makeCollapsible.styles%7Cskins.vector.icons%2Cstyles%7Cskins.vector.search.codex.styles%7Cwikibase.client.init&amp;only=styles&amp;skin=vector-2022">
<script async="" src="/w/load.php?lang=en&amp;modules=startup&amp;only=scripts&amp;raw=1&amp;skin=vector-2022"></script>
<meta name="ResourceLoaderDynamicStyles" content="">
<link rel="stylesheet" href="/w/load.php?lang=en&amp;modules=site.styles&amp;only=styles&amp;skin=vector-2022">
<meta name="generator" content="MediaWiki 1.45.0-wmf.18">
<meta name="referrer" content="origin">
<meta name="referrer" content="origin-when-cross-origin">
<meta name="robots" content="max-image-preview:standard">
<meta name="format-detection" content="telephone=no">
<meta property="og:image" content="https://upload.wikimedia.org/wikipedia/commons/thumb/b/b7/Circular_buffer.svg/1200px-Circular_buffer.svg.png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="1200">
<meta name="viewport" content="width=1120">
<meta property="og:title" content="Circular buffer - Wikipedia">
<meta property="og:type" content="website">
<link rel="preconnect" href="//upload.wikimedia.org">
<link rel="alternate" media="only screen and (max-width: 640px)" href="//en.m.wikipedia.org/wiki/Circular_buffer">
<link rel="alternate" type="application/x-wiki" title="Edit this page" href="/w/index.php?title=Circular_buffer&amp;action=edit">
<link rel="apple-touch-icon" href="/static/apple-touch/wikipedia.png">
<link rel="icon" href="/static/favicon/wikipedia.ico">
<link rel="search" type="application/opensearchdescription+xml" href="/w/rest.php/v1/search" title="Wikipedia (en)">
<link rel="EditURI" type="application/rsd+xml" href="//en.wikipedia.org/w/api.php?action=rsd">
<link rel="canonical" href="https://en.wikipedia.org/wiki/Circular_buffer">
<link rel="license" href="https://creativecommons.org/licenses/by-sa/4.0/deed.en">
<link rel="alternate" type="application/atom+xml" title="Wikipedia Atom feed" href="/w/index.php?title=Special:RecentChanges&amp;feed=atom">
<link rel="dns-prefetch" href="//meta.wikimedia.org" />
<link rel="dns-prefetch" href="auth.wikimedia.org">
</head>
<body class="skin--responsive skin-vector skin-vector-search-vue mediawiki ltr sitedir-ltr mw-hide-empty-elt ns-0 ns-subject mw-editable page-Circular_buffer rootpage-Circular_buffer skin-vector-2022 action-view"><a class="mw-jump-link" href="#bodyContent">Jump to content</a>
<div class="vector-header-container">
	<header class="vector-header mw-header no-font-mode-scale">
		<div class="vector-header-start">
			<nav class="vector-main-menu-landmark" aria-label="Site">
				
<div id="vector-main-menu-dropdown" class="vector-dropdown vector-main-menu-dropdown vector-button-flush-left vector-button-flush-right"  title="Main menu" >
	<input type="checkbox" id="vector-main-menu-dropdown-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-main-menu-dropdown" class="vector-dropdown-checkbox "  aria-label="Main menu"  >
	<label id="vector-main-menu-dropdown-label" for="vector-main-menu-dropdown-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only " aria-hidden="true"  ><span class="vector-icon mw-ui-icon-menu mw-ui-icon-wikimedia-menu"></span>

<span class="vector-dropdown-label-text">Main menu</span>
	</label>
	<div class="vector-dropdown-content">


				<div id="vector-main-menu-unpinned-container" class="vector-unpinned-container">
		
<div id="vector-main-menu" class="vector-main-menu vector-pinnable-element">
	<div
	class="vector-pinnable-header vector-main-menu-pinnable-header vector-pinnable-header-unpinned"
	data-feature-name="main-menu-pinned"
	data-pinnable-element-id="vector-main-menu"
	data-pinned-container-id="vector-main-menu-pinned-container"
	data-unpinned-container-id="vector-main-menu-unpinned-container"
>
	<div class="vector-pinnable-header-label">Main menu</div>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-pin-button" data-event-name="pinnable-header.vector-main-menu.pin">move to sidebar</button>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-unpin-button" data-event-name="pinnable-header.vector-main-menu.unpin">hide</button>
</div>

	
<div id="p-navigation" class="vector-menu mw-portlet mw-portlet-navigation"  >
	<div class="vector-menu-heading">
		Navigation
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="n-mainpage-description" class="mw-list-item"><a href="/wiki/Main_Page" title="Visit the main page [z]" accesskey="z"><span>Main page</span></a></li><li id="n-contents" class="mw-list-item"><a href="/wiki/Wikipedia:Contents" title="Guides to browsing Wikipedia"><span>Contents</span></a></li><li id="n-currentevents" class="mw-list-item"><a href="/wiki/Portal:Current_events" title="Articles related to current events"><span>Current events</span></a></li><li id="n-randompage" class="mw-list-item"><a href="/wiki/Special:Random" title="Visit a randomly selected article [x]" accesskey="x"><span>Random article</span></a></li><li id="n-aboutsite" class="mw-list-item"><a href="/wiki/Wikipedia:About" title="Learn about Wikipedia and how it works"><span>About Wikipedia</span></a></li><li id="n-contactpage" class="mw-list-item"><a href="//en.wikipedia.org/wiki/Wikipedia:Contact_us" title="How to contact Wikipedia"><span>Contact us</span></a></li>
		</ul>
		
	</div>
</div>

	
	
<div id="p-interaction" class="vector-menu mw-portlet mw-portlet-interaction"  >
	<div class="vector-menu-heading">
		Contribute
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="n-help" class="mw-list-item"><a href="/wiki/Help:Contents" title="Guidance on how to use and edit Wikipedia"><span>Help</span></a></li><li id="n-introduction" class="mw-list-item"><a href="/wiki/Help:Introduction" title="Learn how to edit Wikipedia"><span>Learn to edit</span></a></li><li id="n-portal" class="mw-list-item"><a href="/wiki/Wikipedia:Community_portal" title="The hub for editors"><span>Community portal</span></a></li><li id="n-recentchanges" class="mw-list-item"><a href="/wiki/Special:RecentChanges" title="A list of recent changes to Wikipedia [r]" accesskey="r"><span>Recent changes</span></a></li><li id="n-upload" class="mw-list-item"><a href="/wiki/Wikipedia:File_upload_wizard" title="Add images or other media for use on Wikipedia"><span>Upload file</span></a></li><li id="n-specialpages" class="mw-list-item"><a href="/wiki/Special:SpecialPages"><span>Special pages</span></a></li>
		</ul>
		
	</div>
</div>

</div>

				</div>

	</div>
</div>

		</nav>
			
<a href="/wiki/Main_Page" class="mw-logo">
	<img class="mw-logo-icon" src="/static/images/icons/wikipedia.png" alt="" aria-hidden="true" height="50" width="50">
	<span class="mw-logo-container skin-invert">
		<img class="mw-logo-wordmark" alt="Wikipedia" src="/static/images/mobile/copyright/wikipedia-wordmark-en.svg" style="width: 7.5em; height: 1.125em;">
		<img class="mw-logo-tagline" alt="The Free Encyclopedia" src="/static/images/mobile/copyright/wikipedia-tagline-en.svg" width="117" height="13" style="width: 7.3125em; height: 0.8125em;">
	</span>
</a>

		</div>
		<div class="vector-header-end">
			
<div id="p-search" role="search" class="vector-search-box-vue  vector-search-box-collapses vector-search-box-show-thumbnail vector-search-box-auto-expand-width vector-search-box">
	<a href="/wiki/Special:Search" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only search-toggle" title="Search Wikipedia [f]" accesskey="f"><span class="vector-icon mw-ui-icon-search mw-ui-icon-wikimedia-search"></span>

<span>Search</span>
	</a>
	<div class="vector-typeahead-search-container">
		<div class="cdx-typeahead-search cdx-typeahead-search--show-thumbnail cdx-typeahead-search--auto-expand-width">
			<form action="/w/index.php" id="searchform" class="cdx-search-input cdx-search-input--has-end-button">
				<div id="simpleSearch" class="cdx-search-input__input-wrapper"  data-search-loc="header-moved">
					<div class="cdx-text-input cdx-text-input--has-start-icon">
						<input
							class="cdx-text-input__input mw-searchInput" autocomplete="off"
							 type="search" name="search" placeholder="Search Wikipedia" aria-label="Search Wikipedia" autocapitalize="sentences" spellcheck="false" title="Search Wikipedia [f]" accesskey="f" id="searchInput"
							>
						<span class="cdx-text-input__icon cdx-text-input__start-icon"></span>
					</div>
					<input type="hidden" name="title" value="Special:Search">
				</div>
				<button class="cdx-button cdx-search-input__end-button">Search</button>
			</form>
		</div>
	</div>
</div>

			<nav class="vector-user-links vector-user-links-wide" aria-label="Personal tools">
	<div class="vector-user-links-main">
	
<div id="p-vector-user-menu-preferences" class="vector-menu mw-portlet emptyPortlet"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			
		</ul>
		
	</div>
</div>

	
<div id="p-vector-user-menu-userpage" class="vector-menu mw-portlet emptyPortlet"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			
		</ul>
		
	</div>
</div>

	<nav class="vector-appearance-landmark" aria-label="Appearance">
		
<div id="vector-appearance-dropdown" class="vector-dropdown "  title="Change the appearance of the page&#039;s font size, width, and color" >
	<input type="checkbox" id="vector-appearance-dropdown-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-appearance-dropdown" class="vector-dropdown-checkbox "  aria-label="Appearance"  >
	<label id="vector-appearance-dropdown-label" for="vector-appearance-dropdown-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only " aria-hidden="true"  ><span class="vector-icon mw-ui-icon-appearance mw-ui-icon-wikimedia-appearance"></span>

<span class="vector-dropdown-label-text">Appearance</span>
	</label>
	<div class="vector-dropdown-content">


			<div id="vector-appearance-unpinned-container" class="vector-unpinned-container">
				
			</div>
		
	</div>
</div>

	</nav>
	
<div id="p-vector-user-menu-notifications" class="vector-menu mw-portlet emptyPortlet"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			
		</ul>
		
	</div>
</div>

	
<div id="p-vector-user-menu-overflow" class="vector-menu mw-portlet"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			<li id="pt-sitesupport-2" class="user-links-collapsible-item mw-list-item user-links-collapsible-item"><a data-mw="interface" href="https://donate.wikimedia.org/?wmf_source=donate&amp;wmf_medium=sidebar&amp;wmf_campaign=en.wikipedia.org&amp;uselang=en" class=""><span>Donate</span></a>
</li>
<li id="pt-createaccount-2" class="user-links-collapsible-item mw-list-item user-links-collapsible-item"><a data-mw="interface" href="/w/index.php?title=Special:CreateAccount&amp;returnto=Circular+buffer" title="You are encouraged to create an account and log in; however, it is not mandatory" class=""><span>Create account</span></a>
</li>
<li id="pt-login-2" class="user-links-collapsible-item mw-list-item user-links-collapsible-item"><a data-mw="interface" href="/w/index.php?title=Special:UserLogin&amp;returnto=Circular+buffer" title="You&#039;re encouraged to log in; however, it&#039;s not mandatory. [o]" accesskey="o" class=""><span>Log in</span></a>
</li>

			
		</ul>
		
	</div>
</div>

	</div>
	
<div id="vector-user-links-dropdown" class="vector-dropdown vector-user-menu vector-button-flush-right vector-user-menu-logged-out"  title="Log in and more options" >
	<input type="checkbox" id="vector-user-links-dropdown-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-user-links-dropdown" class="vector-dropdown-checkbox "  aria-label="Personal tools"  >
	<label id="vector-user-links-dropdown-label" for="vector-user-links-dropdown-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only " aria-hidden="true"  ><span class="vector-icon mw-ui-icon-ellipsis mw-ui-icon-wikimedia-ellipsis"></span>

<span class="vector-dropdown-label-text">Personal tools</span>
	</label>
	<div class="vector-dropdown-content">


		
<div id="p-personal" class="vector-menu mw-portlet mw-portlet-personal user-links-collapsible-item"  title="User menu" >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="pt-sitesupport" class="user-links-collapsible-item mw-list-item"><a href="https://donate.wikimedia.org/?wmf_source=donate&amp;wmf_medium=sidebar&amp;wmf_campaign=en.wikipedia.org&amp;uselang=en"><span>Donate</span></a></li><li id="pt-createaccount" class="user-links-collapsible-item mw-list-item"><a href="/w/index.php?title=Special:CreateAccount&amp;returnto=Circular+buffer" title="You are encouraged to create an account and log in; however, it is not mandatory"><span class="vector-icon mw-ui-icon-userAdd mw-ui-icon-wikimedia-userAdd"></span> <span>Create account</span></a></li><li id="pt-login" class="user-links-collapsible-item mw-list-item"><a href="/w/index.php?title=Special:UserLogin&amp;returnto=Circular+buffer" title="You&#039;re encouraged to log in; however, it&#039;s not mandatory. [o]" accesskey="o"><span class="vector-icon mw-ui-icon-logIn mw-ui-icon-wikimedia-logIn"></span> <span>Log in</span></a></li>
		</ul>
		
	</div>
</div>

<div id="p-user-menu-anon-editor" class="vector-menu mw-portlet mw-portlet-user-menu-anon-editor"  >
	<div class="vector-menu-heading">
		Pages for logged out editors <a href="/wiki/Help:Introduction" aria-label="Learn more about editing"><span>learn more</span></a>
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="pt-anoncontribs" class="mw-list-item"><a href="/wiki/Special:MyContributions" title="A list of edits made from this IP address [y]" accesskey="y"><span>Contributions</span></a></li><li id="pt-anontalk" class="mw-list-item"><a href="/wiki/Special:MyTalk" title="Discussion about edits from this IP address [n]" accesskey="n"><span>Talk</span></a></li>
		</ul>
		
	</div>
</div>

	
	</div>
</div>

</nav>

		</div>
	</header>
</div>
<div class="mw-page-container">
	<div class="mw-page-container-inner">
		<div class="vector-sitenotice-container">
			<div id="siteNotice"><!-- CentralNotice --></div>
		</div>
		<div class="vector-column-start">
			<div class="vector-main-menu-container">
		<div id="mw-navigation">
			<nav id="mw-panel" class="vector-main-menu-landmark" aria-label="Site">
				<div id="vector-main-menu-pinned-container" class="vector-pinned-container">
				
				</div>
		</nav>
		</div>
	</div>
	<div class="vector-sticky-pinned-container">
				<nav id="mw-panel-toc" aria-label="Contents" data-event-name="ui.sidebar-toc" class="mw-table-of-contents-container vector-toc-landmark">
					<div id="vector-toc-pinned-container" class="vector-pinned-container">
					<div id="vector-toc" class="vector-toc vector-pinnable-element">
	<div
	class="vector-pinnable-header vector-toc-pinnable-header vector-pinnable-header-pinned"
	data-feature-name="toc-pinned"
	data-pinnable-element-id="vector-toc"
	
	
>
	<h2 class="vector-pinnable-header-label">Contents</h2>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-pin-button" data-event-name="pinnable-header.vector-toc.pin">move to sidebar</button>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-unpin-button" data-event-name="pinnable-header.vector-toc.unpin">hide</button>
</div>


	<ul class="vector-toc-contents" id="mw-panel-toc-list">
		<li id="toc-mw-content-text"
			class="vector-toc-list-item vector-toc-level-1">
			<a href="#" class="vector-toc-link">
				<div class="vector-toc-text">(Top)</div>
			</a>
		</li>
		<li id="toc-Overview"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#Overview">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">1</span>
				<span>Overview</span>
			</div>
		</a>
		
		<ul id="toc-Overview-sublist" class="vector-toc-list">
		</ul>
	</li>
	<li id="toc-Uses"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#Uses">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">2</span>
				<span>Uses</span>
			</div>
		</a>
		
		<ul id="toc-Uses-sublist" class="vector-toc-list">
		</ul>
	</li>
	<li id="toc-Circular_buffer_mechanics"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#Circular_buffer_mechanics">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">3</span>
				<span>Circular buffer mechanics</span>
			</div>
		</a>
		
		<ul id="toc-Circular_buffer_mechanics-sublist" class="vector-toc-list">
		</ul>
	</li>
	<li id="toc-Optimization"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#Optimization">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">4</span>
				<span>Optimization</span>
			</div>
		</a>
		
		<ul id="toc-Optimization-sublist" class="vector-toc-list">
		</ul>
	</li>
	<li id="toc-Fixed-length-element_and_contiguous-block_circular_buffer"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#Fixed-length-element_and_contiguous-block_circular_buffer">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">5</span>
				<span>Fixed-length-element and contiguous-block circular buffer</span>
			</div>
		</a>
		
		<ul id="toc-Fixed-length-element_and_contiguous-block_circular_buffer-sublist" class="vector-toc-list">
		</ul>
	</li>
	<li id="toc-References"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#References">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">6</span>
				<span>References</span>
			</div>
		</a>
		
		<ul id="toc-References-sublist" class="vector-toc-list">
		</ul>
	</li>
	<li id="toc-External_links"
		class="vector-toc-list-item vector-toc-level-1 vector-toc-list-item-expanded">
		<a class="vector-toc-link" href="#External_links">
			<div class="vector-toc-text">
				<span class="vector-toc-numb">7</span>
				<span>External links</span>
			</div>
		</a>
		
		<ul id="toc-External_links-sublist" class="vector-toc-list">
		</ul>
	</li>
</ul>
</div>

					</div>
		</nav>
			</div>
		</div>
		<div class="mw-content-container">
			<main id="content" class="mw-body">
				<header class="mw-body-header vector-page-titlebar no-font-mode-scale">
					<nav aria-label="Contents" class="vector-toc-landmark">
						
<div id="vector-page-titlebar-toc" class="vector-dropdown vector-page-titlebar-toc vector-button-flush-left"  title="Table of Contents" >
	<input type="checkbox" id="vector-page-titlebar-toc-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-page-titlebar-toc" class="vector-dropdown-checkbox "  aria-label="Toggle the table of contents"  >
	<label id="vector-page-titlebar-toc-label" for="vector-page-titlebar-toc-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only " aria-hidden="true"  ><span class="vector-icon mw-ui-icon-listBullet mw-ui-icon-wikimedia-listBullet"></span>

<span class="vector-dropdown-label-text">Toggle the table of contents</span>
	</label>
	<div class="vector-dropdown-content">


							<div id="vector-page-titlebar-toc-unpinned-container" class="vector-unpinned-container">
			</div>
		
	</div>
</div>

					</nav>
					<h1 id="firstHeading" class="firstHeading mw-first-heading"><span class="mw-page-title-main">Circular buffer</span></h1>
							
<div id="p-lang-btn" class="vector-dropdown mw-portlet mw-portlet-lang"  >
	<input type="checkbox" id="p-lang-btn-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-p-lang-btn" class="vector-dropdown-checkbox mw-interlanguage-selector" aria-label="Go to an article in another language. Available in 18 languages"   >
	<label id="p-lang-btn-label" for="p-lang-btn-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--action-progressive mw-portlet-lang-heading-18" aria-hidden="true"  ><span class="vector-icon mw-ui-icon-language-progressive mw-ui-icon-wikimedia-language-progressive"></span>

<span class="vector-dropdown-label-text">18 languages</span>
	</label>
	<div class="vector-dropdown-content">

		<div class="vector-menu-content">
			
			<ul class="vector-menu-content-list">
				
				<li class="interlanguage-link interwiki-af mw-list-item"><a href="https://af.wikipedia.org/wiki/Ringbuffer" title="Ringbuffer – Afrikaans" lang="af" hreflang="af" data-title="Ringbuffer" data-language-autonym="Afrikaans" data-language-local-name="Afrikaans" class="interlanguage-link-target"><span>Afrikaans</span></a></li><li class="interlanguage-link interwiki-ca mw-list-item"><a href="https://ca.wikipedia.org/wiki/Tamp%C3%B3_circular" title="Tampó circular – Catalan" lang="ca" hreflang="ca" data-title="Tampó circular" data-language-autonym="Català" data-language-local-name="Catalan" class="interlanguage-link-target"><span>Català</span></a></li><li class="interlanguage-link interwiki-cs mw-list-item"><a href="https://cs.wikipedia.org/wiki/Cyklick%C3%A1_fronta" title="Cyklická fronta – Czech" lang="cs" hreflang="cs" data-title="Cyklická fronta" data-language-autonym="Čeština" data-language-local-name="Czech" class="interlanguage-link-target"><span>Čeština</span></a></li><li class="interlanguage-link interwiki-de mw-list-item"><a href="https://de.wikipedia.org/wiki/Ringpuffer" title="Ringpuffer – German" lang="de" hreflang="de" data-title="Ringpuffer" data-language-autonym="Deutsch" data-language-local-name="German" class="interlanguage-link-target"><span>Deutsch</span></a></li><li class="interlanguage-link interwiki-es mw-list-item"><a href="https://es.wikipedia.org/wiki/Buffer_circular" title="Buffer circular – Spanish" lang="es" hreflang="es" data-title="Buffer circular" data-language-autonym="Español" data-language-local-name="Spanish" class="interlanguage-link-target"><span>Español</span></a></li><li class="interlanguage-link interwiki-fa mw-list-item"><a href="https://fa.wikipedia.org/wiki/%D8%A8%D8%A7%D9%81%D8%B1_%DA%86%D8%B1%D8%AE%D8%B4%DB%8C" title="بافر چرخشی – Persian" lang="fa" hreflang="fa" data-title="بافر چرخشی" data-language-autonym="فارسی" data-language-local-name="Persian" class="interlanguage-link-target"><span>فارسی</span></a></li><li class="interlanguage-link interwiki-fr mw-list-item"><a href="https://fr.wikipedia.org/wiki/Buffer_circulaire" title="Buffer circulaire – French" lang="fr" hreflang="fr" data-title="Buffer circulaire" data-language-autonym="Français" data-language-local-name="French" class="interlanguage-link-target"><span>Français</span></a></li><li class="interlanguage-link interwiki-ko mw-list-item"><a href="https://ko.wikipedia.org/wiki/%EC%9B%90%ED%98%95_%EB%B2%84%ED%8D%BC" title="원형 버퍼 – Korean" lang="ko" hreflang="ko" data-title="원형 버퍼" data-language-autonym="한국어" data-language-local-name="Korean" class="interlanguage-link-target"><span>한국어</span></a></li><li class="interlanguage-link interwiki-id mw-list-item"><a href="https://id.wikipedia.org/wiki/Penyangga_melingkar" title="Penyangga melingkar – Indonesian" lang="id" hreflang="id" data-title="Penyangga melingkar" data-language-autonym="Bahasa Indonesia" data-language-local-name="Indonesian" class="interlanguage-link-target"><span>Bahasa Indonesia</span></a></li><li class="interlanguage-link interwiki-ja mw-list-item"><a href="https://ja.wikipedia.org/wiki/%E3%83%AA%E3%83%B3%E3%82%B0%E3%83%90%E3%83%83%E3%83%95%E3%82%A1" title="リングバッファ – Japanese" lang="ja" hreflang="ja" data-title="リングバッファ" data-language-autonym="日本語" data-language-local-name="Japanese" class="interlanguage-link-target"><span>日本語</span></a></li><li class="interlanguage-link interwiki-pl mw-list-item"><a href="https://pl.wikipedia.org/wiki/Bufor_cykliczny" title="Bufor cykliczny – Polish" lang="pl" hreflang="pl" data-title="Bufor cykliczny" data-language-autonym="Polski" data-language-local-name="Polish" class="interlanguage-link-target"><span>Polski</span></a></li><li class="interlanguage-link interwiki-pt mw-list-item"><a href="https://pt.wikipedia.org/wiki/Circular_buffer" title="Circular buffer – Portuguese" lang="pt" hreflang="pt" data-title="Circular buffer" data-language-autonym="Português" data-language-local-name="Portuguese" class="interlanguage-link-target"><span>Português</span></a></li><li class="interlanguage-link interwiki-ru mw-list-item"><a href="https://ru.wikipedia.org/wiki/%D0%9A%D0%BE%D0%BB%D1%8C%D1%86%D0%B5%D0%B2%D0%BE%D0%B9_%D0%B1%D1%83%D1%84%D0%B5%D1%80" title="Кольцевой буфер – Russian" lang="ru" hreflang="ru" data-title="Кольцевой буфер" data-language-autonym="Русский" data-language-local-name="Russian" class="interlanguage-link-target"><span>Русский</span></a></li><li class="interlanguage-link interwiki-sr mw-list-item"><a href="https://sr.wikipedia.org/wiki/%D0%9A%D1%80%D1%83%D0%B6%D0%BD%D0%B8_%D0%B1%D0%B0%D1%84%D0%B5%D1%80" title="Кружни бафер – Serbian" lang="sr" hreflang="sr" data-title="Кружни бафер" data-language-autonym="Српски / srpski" data-language-local-name="Serbian" class="interlanguage-link-target"><span>Српски / srpski</span></a></li><li class="interlanguage-link interwiki-fi mw-list-item"><a href="https://fi.wikipedia.org/wiki/Rengaspuskuri" title="Rengaspuskuri – Finnish" lang="fi" hreflang="fi" data-title="Rengaspuskuri" data-language-autonym="Suomi" data-language-local-name="Finnish" class="interlanguage-link-target"><span>Suomi</span></a></li><li class="interlanguage-link interwiki-th mw-list-item"><a href="https://th.wikipedia.org/wiki/%E0%B8%9A%E0%B8%B1%E0%B8%9E%E0%B9%80%E0%B8%9F%E0%B8%AD%E0%B8%A3%E0%B9%8C%E0%B8%A7%E0%B8%87%E0%B8%81%E0%B8%A5%E0%B8%A1" title="บัพเฟอร์วงกลม – Thai" lang="th" hreflang="th" data-title="บัพเฟอร์วงกลม" data-language-autonym="ไทย" data-language-local-name="Thai" class="interlanguage-link-target"><span>ไทย</span></a></li><li class="interlanguage-link interwiki-uk mw-list-item"><a href="https://uk.wikipedia.org/wiki/%D0%A6%D0%B8%D0%BA%D0%BB%D1%96%D1%87%D0%BD%D0%B8%D0%B9_%D0%B1%D1%83%D1%84%D0%B5%D1%80" title="Циклічний буфер – Ukrainian" lang="uk" hreflang="uk" data-title="Циклічний буфер" data-language-autonym="Українська" data-language-local-name="Ukrainian" class="interlanguage-link-target"><span>Українська</span></a></li><li class="interlanguage-link interwiki-zh mw-list-item"><a href="https://zh.wikipedia.org/wiki/%E7%92%B0%E5%BD%A2%E7%B7%A9%E8%A1%9D%E5%8D%80" title="環形緩衝區 – Chinese" lang="zh" hreflang="zh" data-title="環形緩衝區" data-language-autonym="中文" data-language-local-name="Chinese" class="interlanguage-link-target"><span>中文</span></a></li>
			</ul>
			<div class="after-portlet after-portlet-lang"><span class="wb-langlinks-edit wb-langlinks-link"><a href="https://www.wikidata.org/wiki/Special:EntityPage/Q1224994#sitelinks-wikipedia" title="Edit interlanguage links" class="wbc-editpage">Edit links</a></span></div>
		</div>

	</div>
</div>
</header>
				<div class="vector-page-toolbar vector-feature-custom-font-size-clientpref--excluded">
					<div class="vector-page-toolbar-container">
						<div id="left-navigation">
							<nav aria-label="Namespaces">
								
<div id="p-associated-pages" class="vector-menu vector-menu-tabs mw-portlet mw-portlet-associated-pages"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="ca-nstab-main" class="selected vector-tab-noicon mw-list-item"><a href="/wiki/Circular_buffer" title="View the content page [c]" accesskey="c"><span>Article</span></a></li><li id="ca-talk" class="vector-tab-noicon mw-list-item"><a href="/wiki/Talk:Circular_buffer" rel="discussion" title="Discuss improvements to the content page [t]" accesskey="t"><span>Talk</span></a></li>
		</ul>
		
	</div>
</div>

								
<div id="vector-variants-dropdown" class="vector-dropdown emptyPortlet"  >
	<input type="checkbox" id="vector-variants-dropdown-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-variants-dropdown" class="vector-dropdown-checkbox " aria-label="Change language variant"   >
	<label id="vector-variants-dropdown-label" for="vector-variants-dropdown-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet" aria-hidden="true"  ><span class="vector-dropdown-label-text">English</span>
	</label>
	<div class="vector-dropdown-content">


					
<div id="p-variants" class="vector-menu mw-portlet mw-portlet-variants emptyPortlet"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			
		</ul>
		
	</div>
</div>

				
	</div>
</div>

							</nav>
						</div>
						<div id="right-navigation" class="vector-collapsible">
							<nav aria-label="Views">
								
<div id="p-views" class="vector-menu vector-menu-tabs mw-portlet mw-portlet-views"  >
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="ca-view" class="selected vector-tab-noicon mw-list-item"><a href="/wiki/Circular_buffer"><span>Read</span></a></li><li id="ca-edit" class="vector-tab-noicon mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;action=edit" title="Edit this page [e]" accesskey="e"><span>Edit</span></a></li><li id="ca-history" class="vector-tab-noicon mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;action=history" title="Past revisions of this page [h]" accesskey="h"><span>View history</span></a></li>
		</ul>
		
	</div>
</div>

							</nav>
				
							<nav class="vector-page-tools-landmark" aria-label="Page tools">
								
<div id="vector-page-tools-dropdown" class="vector-dropdown vector-page-tools-dropdown"  >
	<input type="checkbox" id="vector-page-tools-dropdown-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-page-tools-dropdown" class="vector-dropdown-checkbox "  aria-label="Tools"  >
	<label id="vector-page-tools-dropdown-label" for="vector-page-tools-dropdown-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet" aria-hidden="true"  ><span class="vector-dropdown-label-text">Tools</span>
	</label>
	<div class="vector-dropdown-content">


									<div id="vector-page-tools-unpinned-container" class="vector-unpinned-container">
						
<div id="vector-page-tools" class="vector-page-tools vector-pinnable-element">
	<div
	class="vector-pinnable-header vector-page-tools-pinnable-header vector-pinnable-header-unpinned"
	data-feature-name="page-tools-pinned"
	data-pinnable-element-id="vector-page-tools"
	data-pinned-container-id="vector-page-tools-pinned-container"
	data-unpinned-container-id="vector-page-tools-unpinned-container"
>
	<div class="vector-pinnable-header-label">Tools</div>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-pin-button" data-event-name="pinnable-header.vector-page-tools.pin">move to sidebar</button>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-unpin-button" data-event-name="pinnable-header.vector-page-tools.unpin">hide</button>
</div>

	
<div id="p-cactions" class="vector-menu mw-portlet mw-portlet-cactions emptyPortlet vector-has-collapsible-items"  title="More options" >
	<div class="vector-menu-heading">
		Actions
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="ca-more-view" class="selected vector-more-collapsible-item mw-list-item"><a href="/wiki/Circular_buffer"><span>Read</span></a></li><li id="ca-more-edit" class="vector-more-collapsible-item mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;action=edit" title="Edit this page [e]" accesskey="e"><span>Edit</span></a></li><li id="ca-more-history" class="vector-more-collapsible-item mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;action=history"><span>View history</span></a></li>
		</ul>
		
	</div>
</div>

<div id="p-tb" class="vector-menu mw-portlet mw-portlet-tb"  >
	<div class="vector-menu-heading">
		General
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="t-whatlinkshere" class="mw-list-item"><a href="/wiki/Special:WhatLinksHere/Circular_buffer" title="List of all English Wikipedia pages containing links to this page [j]" accesskey="j"><span>What links here</span></a></li><li id="t-recentchangeslinked" class="mw-list-item"><a href="/wiki/Special:RecentChangesLinked/Circular_buffer" rel="nofollow" title="Recent changes in pages linked from this page [k]" accesskey="k"><span>Related changes</span></a></li><li id="t-upload" class="mw-list-item"><a href="//en.wikipedia.org/wiki/Wikipedia:File_Upload_Wizard" title="Upload files [u]" accesskey="u"><span>Upload file</span></a></li><li id="t-permalink" class="mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;oldid=1284869780" title="Permanent link to this revision of this page"><span>Permanent link</span></a></li><li id="t-info" class="mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;action=info" title="More information about this page"><span>Page information</span></a></li><li id="t-cite" class="mw-list-item"><a href="/w/index.php?title=Special:CiteThisPage&amp;page=Circular_buffer&amp;id=1284869780&amp;wpFormIdentifier=titleform" title="Information on how to cite this page"><span>Cite this page</span></a></li><li id="t-urlshortener" class="mw-list-item"><a href="/w/index.php?title=Special:UrlShortener&amp;url=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FCircular_buffer"><span>Get shortened URL</span></a></li><li id="t-urlshortener-qrcode" class="mw-list-item"><a href="/w/index.php?title=Special:QrCode&amp;url=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FCircular_buffer"><span>Download QR code</span></a></li>
		</ul>
		
	</div>
</div>

<div id="p-coll-print_export" class="vector-menu mw-portlet mw-portlet-coll-print_export"  >
	<div class="vector-menu-heading">
		Print/export
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li id="coll-download-as-rl" class="mw-list-item"><a href="/w/index.php?title=Special:DownloadAsPdf&amp;page=Circular_buffer&amp;action=show-download-screen" title="Download this page as a PDF file"><span>Download as PDF</span></a></li><li id="t-print" class="mw-list-item"><a href="/w/index.php?title=Circular_buffer&amp;printable=yes" title="Printable version of this page [p]" accesskey="p"><span>Printable version</span></a></li>
		</ul>
		
	</div>
</div>

<div id="p-wikibase-otherprojects" class="vector-menu mw-portlet mw-portlet-wikibase-otherprojects"  >
	<div class="vector-menu-heading">
		In other projects
	</div>
	<div class="vector-menu-content">
		
		<ul class="vector-menu-content-list">
			
			<li class="wb-otherproject-link wb-otherproject-commons mw-list-item"><a href="https://commons.wikimedia.org/wiki/Category:Circular_buffers" hreflang="en"><span>Wikimedia Commons</span></a></li><li id="t-wikibase" class="wb-otherproject-link wb-otherproject-wikibase-dataitem mw-list-item"><a href="https://www.wikidata.org/wiki/Special:EntityPage/Q1224994" title="Structured data on this page hosted by Wikidata [g]" accesskey="g"><span>Wikidata item</span></a></li>
		</ul>
		
	</div>
</div>

</div>

									</div>
				
	</div>
</div>

							</nav>
						</div>
					</div>
				</div>
				<div class="vector-column-end no-font-mode-scale">
					<div class="vector-sticky-pinned-container">
						<nav class="vector-page-tools-landmark" aria-label="Page tools">
							<div id="vector-page-tools-pinned-container" class="vector-pinned-container">
				
							</div>
		</nav>
						<nav class="vector-appearance-landmark" aria-label="Appearance">
							<div id="vector-appearance-pinned-container" class="vector-pinned-container">
				<div id="vector-appearance" class="vector-appearance vector-pinnable-element">
	<div
	class="vector-pinnable-header vector-appearance-pinnable-header vector-pinnable-header-pinned"
	data-feature-name="appearance-pinned"
	data-pinnable-element-id="vector-appearance"
	data-pinned-container-id="vector-appearance-pinned-container"
	data-unpinned-container-id="vector-appearance-unpinned-container"
>
	<div class="vector-pinnable-header-label">Appearance</div>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-pin-button" data-event-name="pinnable-header.vector-appearance.pin">move to sidebar</button>
	<button class="vector-pinnable-header-toggle-button vector-pinnable-header-unpin-button" data-event-name="pinnable-header.vector-appearance.unpin">hide</button>
</div>


</div>

							</div>
		</nav>
					</div>
				</div>
				<div id="bodyContent" class="vector-body" aria-labelledby="firstHeading" data-mw-ve-target-container>
					<div class="vector-body-before-content">
							<div class="mw-indicators">
		</div>

						<div id="siteSub" class="noprint">From Wikipedia, the free encyclopedia</div>
					</div>
					<div id="contentSub"><div id="mw-content-subtitle"></div></div>
					
					
					<div id="mw-content-text" class="mw-body-content"><div class="mw-content-ltr mw-parser-output" lang="en" dir="ltr"><div class="shortdescription nomobile noexcerpt noprint searchaux" style="display:none">Data structure in computer science</div>
<figure typeof="mw:File/Thumb"><a href="/wiki/File:Circular_buffer.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/b/b7/Circular_buffer.svg/200px-Circular_buffer.svg.png" decoding="async" width="200" height="200" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/b/b7/Circular_buffer.svg/300px-Circular_buffer.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/b/b7/Circular_buffer.svg/400px-Circular_buffer.svg.png 2x" data-file-width="200" data-file-height="200" /></a><figcaption>A ring showing, conceptually, a circular buffer. This visually shows that the buffer has no real end and it can loop around the buffer. However, since memory is never physically created as a ring, a linear representation is generally used as is done below.</figcaption></figure>
<p>In <a href="/wiki/Computer_science" title="Computer science">computer science</a>, a <b>circular buffer</b>, <b>circular queue</b>, <b>cyclic buffer</b> or <b>ring buffer</b> is a <a href="/wiki/Data_structure" title="Data structure">data structure</a> that uses a single, fixed-size <a href="/wiki/Buffer_(computer_science)" class="mw-redirect" title="Buffer (computer science)">buffer</a> as if it were connected end-to-end. This structure lends itself easily to buffering <a href="/wiki/Data_stream" title="Data stream">data streams</a>.<sup id="cite_ref-1" class="reference"><a href="#cite_note-1"><span class="cite-bracket">&#91;</span>1<span class="cite-bracket">&#93;</span></a></sup> There were early circular buffer implementations in hardware.<sup id="cite_ref-2" class="reference"><a href="#cite_note-2"><span class="cite-bracket">&#91;</span>2<span class="cite-bracket">&#93;</span></a></sup><sup id="cite_ref-3" class="reference"><a href="#cite_note-3"><span class="cite-bracket">&#91;</span>3<span class="cite-bracket">&#93;</span></a></sup>
</p>
<meta property="mw:PageProp/toc" />
<div class="mw-heading mw-heading2"><h2 id="Overview">Overview</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=1" title="Edit section: Overview"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<figure typeof="mw:File/Thumb"><a href="/wiki/File:Circular_Buffer_Animation.gif" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/f/fd/Circular_Buffer_Animation.gif/500px-Circular_Buffer_Animation.gif" decoding="async" width="400" height="300" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/f/fd/Circular_Buffer_Animation.gif/600px-Circular_Buffer_Animation.gif 1.5x, //upload.wikimedia.org/wikipedia/commons/f/fd/Circular_Buffer_Animation.gif 2x" data-file-width="800" data-file-height="600" /></a><figcaption>A 24-byte keyboard circular buffer. When the write pointer is about to reach the read pointer&#8212;because the microprocessor is not responding&#8212;the buffer stops recording keystrokes. On some computers a beep would be played.</figcaption></figure>
<p>A circular buffer first starts out empty and has a set length. In the diagram below is a 7-element buffer:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_empty.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/f/f7/Circular_buffer_-_empty.svg/250px-Circular_buffer_-_empty.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/f/f7/Circular_buffer_-_empty.svg/375px-Circular_buffer_-_empty.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/f/f7/Circular_buffer_-_empty.svg/500px-Circular_buffer_-_empty.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>Assume that 1 is written in the center of a circular buffer (the exact starting location is not important in a circular buffer):
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_XX1XXXX.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/8/89/Circular_buffer_-_XX1XXXX.svg/250px-Circular_buffer_-_XX1XXXX.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/8/89/Circular_buffer_-_XX1XXXX.svg/375px-Circular_buffer_-_XX1XXXX.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/8/89/Circular_buffer_-_XX1XXXX.svg/500px-Circular_buffer_-_XX1XXXX.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>Then assume that two more elements are added to the circular buffer &#8212; 2 &amp; 3 &#8212; which get put after 1:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_XX123XX.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/d/d7/Circular_buffer_-_XX123XX.svg/250px-Circular_buffer_-_XX123XX.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/d/d7/Circular_buffer_-_XX123XX.svg/375px-Circular_buffer_-_XX123XX.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/d/d7/Circular_buffer_-_XX123XX.svg/500px-Circular_buffer_-_XX123XX.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>If two elements are removed, the two oldest values inside of the circular buffer would be removed. Circular buffers use FIFO (<i><a href="/wiki/First_in,_first_out_(computing)" class="mw-redirect" title="First in, first out (computing)">first in, first out</a></i>) logic. In the example, 1 &amp; 2 were the first to enter the circular buffer, they are the first to be removed, leaving 3 inside of the buffer.
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_XXXX3XX.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/1/11/Circular_buffer_-_XXXX3XX.svg/250px-Circular_buffer_-_XXXX3XX.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/1/11/Circular_buffer_-_XXXX3XX.svg/375px-Circular_buffer_-_XXXX3XX.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/1/11/Circular_buffer_-_XXXX3XX.svg/500px-Circular_buffer_-_XXXX3XX.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>If the buffer has 7 elements, then it is completely full:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_6789345.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/6/67/Circular_buffer_-_6789345.svg/250px-Circular_buffer_-_6789345.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/6/67/Circular_buffer_-_6789345.svg/375px-Circular_buffer_-_6789345.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/6/67/Circular_buffer_-_6789345.svg/500px-Circular_buffer_-_6789345.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>A property of the circular buffer is that when it is full and a subsequent write is performed, then it starts overwriting the oldest data. In the current example, two more elements &#8212; A &amp; B &#8212; are added and they <i>overwrite</i> the 3 &amp; 4:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_6789AB5.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/b/ba/Circular_buffer_-_6789AB5.svg/250px-Circular_buffer_-_6789AB5.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/b/ba/Circular_buffer_-_6789AB5.svg/375px-Circular_buffer_-_6789AB5.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/b/ba/Circular_buffer_-_6789AB5.svg/500px-Circular_buffer_-_6789AB5.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>Alternatively, the routines that manage the buffer could prevent overwriting the data and return an error or raise an <a href="/wiki/Exception_handling" title="Exception handling">exception</a>. Whether or not data is overwritten is up to the semantics of the buffer routines or the application using the circular buffer.
</p><p>Finally, if two elements are now removed then what would be removed is <b>not</b> A &amp; B, but 5 &amp; 6 because 5 &amp; 6 are now the oldest elements, yielding the buffer with:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_X789ABX.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/4/43/Circular_buffer_-_X789ABX.svg/250px-Circular_buffer_-_X789ABX.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/4/43/Circular_buffer_-_X789ABX.svg/375px-Circular_buffer_-_X789ABX.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/4/43/Circular_buffer_-_X789ABX.svg/500px-Circular_buffer_-_X789ABX.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<div class="mw-heading mw-heading2"><h2 id="Uses">Uses</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=2" title="Edit section: Uses"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<p>The useful property of a circular buffer is that it does not need to have its elements shuffled around when one is consumed. (If a non-circular buffer were used then it would be necessary to shift all elements when one is consumed.) In other words, the circular buffer is well-suited as a <a href="/wiki/FIFO_(computing_and_electronics)" title="FIFO (computing and electronics)">FIFO</a> (<i>first in, first out</i>) buffer while a standard, non-circular buffer is well suited as a <a href="/wiki/LIFO_(computing)" class="mw-redirect" title="LIFO (computing)">LIFO</a> (<i>last in, first out</i>) buffer.
</p><p>Circular buffering makes a good implementation strategy for a <a href="/wiki/Queue_(data_structure)" class="mw-redirect" title="Queue (data structure)">queue</a> that has fixed maximum size. Should a maximum size be adopted for a queue, then a circular buffer is a completely ideal implementation; all queue operations are constant time. However, expanding a circular buffer requires shifting memory, which is comparatively costly. For arbitrarily expanding queues, a <a href="/wiki/Linked_list" title="Linked list">linked list</a> approach may be preferred instead.
</p><p>In some situations, overwriting circular buffer can be used, e.g. in multimedia. If the buffer is used as the bounded buffer in the <a href="/wiki/Producer%E2%80%93consumer_problem" title="Producer–consumer problem">producer–consumer problem</a> then it is probably desired for the producer (e.g., an audio generator) to overwrite old data if the consumer (e.g., the <a href="/wiki/Sound_card" title="Sound card">sound card</a>) is unable to momentarily keep up. Also, the <a href="/wiki/LZ77" class="mw-redirect" title="LZ77">LZ77</a> family of lossless data compression algorithms operates on the assumption that strings seen more recently in a data stream are more likely to occur soon in the stream. Implementations store the most recent data in a circular buffer.
</p>
<div class="mw-heading mw-heading2"><h2 id="Circular_buffer_mechanics">Circular buffer mechanics</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=3" title="Edit section: Circular buffer mechanics"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<dl><dd><figure typeof="mw:File/Thumb"><a href="/wiki/File:Hardware_circular_buffer_implementation_patent_us3979733_fig4.png" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/2/29/Hardware_circular_buffer_implementation_patent_us3979733_fig4.png/250px-Hardware_circular_buffer_implementation_patent_us3979733_fig4.png" decoding="async" width="250" height="151" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/2/29/Hardware_circular_buffer_implementation_patent_us3979733_fig4.png/500px-Hardware_circular_buffer_implementation_patent_us3979733_fig4.png 1.5x" data-file-width="2784" data-file-height="1676" /></a><figcaption>Circular buffer implementation in hardware, US patent 3979733, fig4</figcaption></figure></dd></dl>
<p>A circular buffer can be implemented using a <a href="/wiki/Pointer_(computer_programming)" title="Pointer (computer programming)">pointer</a> and four integers:<sup id="cite_ref-Liu_Wu_Das_2021_p._117_4-0" class="reference"><a href="#cite_note-Liu_Wu_Das_2021_p._117-4"><span class="cite-bracket">&#91;</span>4<span class="cite-bracket">&#93;</span></a></sup>
</p>
<ul><li>buffer start in memory</li>
<li>buffer capacity (length)</li>
<li>write to buffer index (end)</li>
<li>read from buffer index (start)</li></ul>
<p>This image shows a partially full buffer with Length = 7:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_XX123XX_with_pointers.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/0/02/Circular_buffer_-_XX123XX_with_pointers.svg/250px-Circular_buffer_-_XX123XX_with_pointers.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/0/02/Circular_buffer_-_XX123XX_with_pointers.svg/375px-Circular_buffer_-_XX123XX_with_pointers.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/0/02/Circular_buffer_-_XX123XX_with_pointers.svg/500px-Circular_buffer_-_XX123XX_with_pointers.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>This image shows a full buffer with four elements (numbers 1 through 4) having been overwritten:
</p>
<dl><dd><span typeof="mw:File"><a href="/wiki/File:Circular_buffer_-_6789AB5_with_pointers.svg" class="mw-file-description"><img src="//upload.wikimedia.org/wikipedia/commons/thumb/0/05/Circular_buffer_-_6789AB5_with_pointers.svg/250px-Circular_buffer_-_6789AB5_with_pointers.svg.png" decoding="async" width="250" height="54" class="mw-file-element" srcset="//upload.wikimedia.org/wikipedia/commons/thumb/0/05/Circular_buffer_-_6789AB5_with_pointers.svg/375px-Circular_buffer_-_6789AB5_with_pointers.svg.png 1.5x, //upload.wikimedia.org/wikipedia/commons/thumb/0/05/Circular_buffer_-_6789AB5_with_pointers.svg/500px-Circular_buffer_-_6789AB5_with_pointers.svg.png 2x" data-file-width="390" data-file-height="85" /></a></span></dd></dl>
<p>In the beginning the indexes end and start are set to 0. The circular buffer write operation writes an element to the end index position and the end index is incremented to the next buffer position. The circular buffer read operation reads an element from the start index position and the start index is incremented to the next buffer position.
</p><p>The start and end indexes alone are not enough to distinguish between buffer full or empty state while also utilizing all buffer slots,<sup id="cite_ref-5" class="reference"><a href="#cite_note-5"><span class="cite-bracket">&#91;</span>5<span class="cite-bracket">&#93;</span></a></sup> but can be if the buffer only has a maximum in-use size of Length − 1.<sup id="cite_ref-6" class="reference"><a href="#cite_note-6"><span class="cite-bracket">&#91;</span>6<span class="cite-bracket">&#93;</span></a></sup> In this case, the buffer is empty if the start and end indexes are equal and full when the in-use size is Length − 1.
Another solution is to have another integer count that is incremented at a write operation and decremented at a read operation. Then checking for emptiness means testing count equals 0 and checking for fullness means testing count equals Length.<sup id="cite_ref-7" class="reference"><a href="#cite_note-7"><span class="cite-bracket">&#91;</span>7<span class="cite-bracket">&#93;</span></a></sup>
</p><p>The following source code is a <a href="/wiki/C_(programming_language)" title="C (programming language)">C</a> implementation together with a minimal test. Function put() puts an item in the buffer, function get() gets an item from the buffer. Both functions take care about the capacity of the buffer&#160;:
</p>
<div class="mw-highlight mw-highlight-lang-c mw-content-ltr" dir="ltr"><pre><span></span><span class="cp">#include</span><span class="w"> </span><span class="cpf">&lt;stdio.h&gt;</span>

<span class="k">enum</span><span class="w"> </span><span class="p">{</span><span class="w"> </span><span class="n">N</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="mi">10</span><span class="w"> </span><span class="p">};</span><span class="w">  </span><span class="c1">// size of circular buffer</span>

<span class="kt">int</span><span class="w"> </span><span class="n">buffer</span><span class="w"> </span><span class="p">[</span><span class="n">N</span><span class="p">];</span><span class="w"> </span><span class="c1">// note: only (N - 1) elements can be stored at a given time</span>
<span class="kt">int</span><span class="w"> </span><span class="n">writeIndx</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="mi">0</span><span class="p">;</span>
<span class="kt">int</span><span class="w"> </span><span class="n">readIndx</span><span class="w">  </span><span class="o">=</span><span class="w"> </span><span class="mi">0</span><span class="p">;</span>

<span class="kt">int</span><span class="w"> </span><span class="nf">put</span><span class="w"> </span><span class="p">(</span><span class="kt">int</span><span class="w"> </span><span class="n">item</span><span class="p">)</span><span class="w"> </span>
<span class="p">{</span>
<span class="w">  </span><span class="k">if</span><span class="w"> </span><span class="p">((</span><span class="n">writeIndx</span><span class="w"> </span><span class="o">+</span><span class="w"> </span><span class="mi">1</span><span class="p">)</span><span class="w"> </span><span class="o">%</span><span class="w"> </span><span class="n">N</span><span class="w"> </span><span class="o">==</span><span class="w"> </span><span class="n">readIndx</span><span class="p">)</span>
<span class="w">  </span><span class="p">{</span>
<span class="w">     </span><span class="c1">// buffer is full, avoid overflow</span>
<span class="w">     </span><span class="k">return</span><span class="w"> </span><span class="mi">0</span><span class="p">;</span>
<span class="w">  </span><span class="p">}</span>
<span class="w">  </span><span class="n">buffer</span><span class="p">[</span><span class="n">writeIndx</span><span class="p">]</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="n">item</span><span class="p">;</span>
<span class="w">  </span><span class="n">writeIndx</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="p">(</span><span class="n">writeIndx</span><span class="w"> </span><span class="o">+</span><span class="w"> </span><span class="mi">1</span><span class="p">)</span><span class="w"> </span><span class="o">%</span><span class="w"> </span><span class="n">N</span><span class="p">;</span>
<span class="w">  </span><span class="k">return</span><span class="w"> </span><span class="mi">1</span><span class="p">;</span>
<span class="p">}</span>

<span class="kt">int</span><span class="w"> </span><span class="nf">get</span><span class="w"> </span><span class="p">(</span><span class="kt">int</span><span class="w"> </span><span class="o">*</span><span class="w"> </span><span class="n">value</span><span class="p">)</span><span class="w"> </span>
<span class="p">{</span>
<span class="w">  </span><span class="k">if</span><span class="w"> </span><span class="p">(</span><span class="n">readIndx</span><span class="w"> </span><span class="o">==</span><span class="w"> </span><span class="n">writeIndx</span><span class="p">)</span>
<span class="w">  </span><span class="p">{</span>
<span class="w">     </span><span class="c1">// buffer is empty</span>
<span class="w">     </span><span class="k">return</span><span class="w"> </span><span class="mi">0</span><span class="p">;</span>
<span class="w">  </span><span class="p">}</span>

<span class="w">  </span><span class="o">*</span><span class="n">value</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="n">buffer</span><span class="p">[</span><span class="n">readIndx</span><span class="p">];</span>
<span class="w">  </span><span class="n">readIndx</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="p">(</span><span class="n">readIndx</span><span class="w"> </span><span class="o">+</span><span class="w"> </span><span class="mi">1</span><span class="p">)</span><span class="w"> </span><span class="o">%</span><span class="w"> </span><span class="n">N</span><span class="p">;</span>
<span class="w">  </span><span class="k">return</span><span class="w"> </span><span class="mi">1</span><span class="p">;</span>
<span class="p">}</span>

<span class="kt">int</span><span class="w"> </span><span class="nf">main</span><span class="w"> </span><span class="p">()</span>
<span class="p">{</span>
<span class="w">  </span><span class="c1">// test circular buffer</span>
<span class="w">  </span><span class="kt">int</span><span class="w"> </span><span class="n">value</span><span class="w"> </span><span class="o">=</span><span class="w"> </span><span class="mi">1001</span><span class="p">;</span>
<span class="w">  </span><span class="k">while</span><span class="w"> </span><span class="p">(</span><span class="n">put</span><span class="w"> </span><span class="p">(</span><span class="n">value</span><span class="w"> </span><span class="o">++</span><span class="p">));</span>
<span class="w">  </span><span class="k">while</span><span class="w"> </span><span class="p">(</span><span class="n">get</span><span class="w"> </span><span class="p">(</span><span class="o">&amp;</span><span class="w"> </span><span class="n">value</span><span class="p">))</span>
<span class="w">     </span><span class="n">printf</span><span class="w"> </span><span class="p">(</span><span class="s">&quot;read %d</span><span class="se">\n</span><span class="s">&quot;</span><span class="p">,</span><span class="w"> </span><span class="n">value</span><span class="p">);</span>
<span class="w">  </span><span class="k">return</span><span class="w"> </span><span class="mi">0</span><span class="p">;</span>
<span class="p">}</span>
</pre></div>
<div class="mw-heading mw-heading2"><h2 id="Optimization">Optimization</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=4" title="Edit section: Optimization"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<p>A circular-buffer implementation may be optimized by <a href="/wiki/Mmap" title="Mmap">mapping</a> the underlying buffer to two contiguous regions of <a href="/wiki/Virtual_memory" title="Virtual memory">virtual memory</a>.<sup id="cite_ref-8" class="reference"><a href="#cite_note-8"><span class="cite-bracket">&#91;</span>8<span class="cite-bracket">&#93;</span></a></sup><sup class="noprint Inline-Template" style="white-space:nowrap;">&#91;<i><a href="/wiki/Wikipedia:Disputed_statement" class="mw-redirect" title="Wikipedia:Disputed statement"><span title="This claim has reliable sources with contradicting facts (January 2022)">disputed</span></a>&#32;&#8211; <a href="/wiki/Talk:Circular_buffer#Optimization" title="Talk:Circular buffer">discuss</a></i>&#93;</sup> (Naturally, the underlying buffer‘s length must then equal some multiple of the system’s <a href="/wiki/Page_(computing)" class="mw-redirect" title="Page (computing)">page size</a>.) Reading from and writing to the circular buffer may then be carried out with greater efficiency by means of direct memory access; those accesses which fall beyond the end of the first virtual-memory region will automatically wrap around to the beginning of the underlying buffer. When the read offset is advanced into the second virtual-memory region, both offsets—read and write—are decremented by the length of the underlying buffer.
</p>
<div class="mw-heading mw-heading2"><h2 id="Fixed-length-element_and_contiguous-block_circular_buffer">Fixed-length-element and contiguous-block circular buffer</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=5" title="Edit section: Fixed-length-element and contiguous-block circular buffer"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<p>Perhaps the most common version of the circular buffer uses 8-bit bytes as elements.
</p><p>Some implementations of the circular buffer use fixed-length elements that are bigger than 8-bit bytes—16-bit integers for audio buffers, 53-byte <a href="/wiki/Asynchronous_Transfer_Mode" title="Asynchronous Transfer Mode">ATM</a> cells for telecom buffers, etc. Each item is contiguous and has the correct <a href="/wiki/Data_alignment" class="mw-redirect" title="Data alignment">data alignment</a>, so software reading and writing these values can be faster than software that handles non-contiguous and non-aligned values.
</p><p><a href="/wiki/Ping-pong_buffer" class="mw-redirect" title="Ping-pong buffer">Ping-pong buffering</a> can be considered a very specialized circular buffer with exactly two large fixed-length elements.
</p><p>The <i>bip buffer</i> (bipartite buffer) is very similar to a circular buffer, except it always returns contiguous blocks which can be variable length. This offers nearly all the efficiency advantages of a circular buffer while maintaining the ability for the buffer to be used in APIs that only accept contiguous blocks.<sup id="cite_ref-cooke_9-0" class="reference"><a href="#cite_note-cooke-9"><span class="cite-bracket">&#91;</span>9<span class="cite-bracket">&#93;</span></a></sup>
</p><p>Fixed-sized compressed circular buffers use an alternative indexing strategy based on elementary number theory to maintain a fixed-sized compressed representation of the entire data sequence.<sup id="cite_ref-gunther_10-0" class="reference"><a href="#cite_note-gunther-10"><span class="cite-bracket">&#91;</span>10<span class="cite-bracket">&#93;</span></a></sup>
</p>
<div class="mw-heading mw-heading2"><h2 id="References">References</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=6" title="Edit section: References"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<style data-mw-deduplicate="TemplateStyles:r1239543626">.mw-parser-output .reflist{margin-bottom:0.5em;list-style-type:decimal}@media screen{.mw-parser-output .reflist{font-size:90%}}.mw-parser-output .reflist .references{font-size:100%;margin-bottom:0;list-style-type:inherit}.mw-parser-output .reflist-columns-2{column-width:30em}.mw-parser-output .reflist-columns-3{column-width:25em}.mw-parser-output .reflist-columns{margin-top:0.3em}.mw-parser-output .reflist-columns ol{margin-top:0}.mw-parser-output .reflist-columns li{page-break-inside:avoid;break-inside:avoid-column}.mw-parser-output .reflist-upper-alpha{list-style-type:upper-alpha}.mw-parser-output .reflist-upper-roman{list-style-type:upper-roman}.mw-parser-output .reflist-lower-alpha{list-style-type:lower-alpha}.mw-parser-output .reflist-lower-greek{list-style-type:lower-greek}.mw-parser-output .reflist-lower-roman{list-style-type:lower-roman}</style><div class="reflist">
<div class="mw-references-wrap"><ol class="references">
<li id="cite_note-1"><span class="mw-cite-backlink"><b><a href="#cite_ref-1">^</a></b></span> <span class="reference-text"><style data-mw-deduplicate="TemplateStyles:r1238218222">.mw-parser-output cite.citation{font-style:inherit;word-wrap:break-word}.mw-parser-output .citation q{quotes:"\"""\"""'""'"}.mw-parser-output .citation:target{background-color:rgba(0,127,255,0.133)}.mw-parser-output .id-lock-free.id-lock-free a{background:url("//upload.wikimedia.org/wikipedia/commons/6/65/Lock-green.svg")right 0.1em center/9px no-repeat}.mw-parser-output .id-lock-limited.id-lock-limited a,.mw-parser-output .id-lock-registration.id-lock-registration a{background:url("//upload.wikimedia.org/wikipedia/commons/d/d6/Lock-gray-alt-2.svg")right 0.1em center/9px no-repeat}.mw-parser-output .id-lock-subscription.id-lock-subscription a{background:url("//upload.wikimedia.org/wikipedia/commons/a/aa/Lock-red-alt-2.svg")right 0.1em center/9px no-repeat}.mw-parser-output .cs1-ws-icon a{background:url("//upload.wikimedia.org/wikipedia/commons/4/4c/Wikisource-logo.svg")right 0.1em center/12px no-repeat}body:not(.skin-timeless):not(.skin-minerva) .mw-parser-output .id-lock-free a,body:not(.skin-timeless):not(.skin-minerva) .mw-parser-output .id-lock-limited a,body:not(.skin-timeless):not(.skin-minerva) .mw-parser-output .id-lock-registration a,body:not(.skin-timeless):not(.skin-minerva) .mw-parser-output .id-lock-subscription a,body:not(.skin-timeless):not(.skin-minerva) .mw-parser-output .cs1-ws-icon a{background-size:contain;padding:0 1em 0 0}.mw-parser-output .cs1-code{color:inherit;background:inherit;border:none;padding:inherit}.mw-parser-output .cs1-hidden-error{display:none;color:var(--color-error,#d33)}.mw-parser-output .cs1-visible-error{color:var(--color-error,#d33)}.mw-parser-output .cs1-maint{display:none;color:#085;margin-left:0.3em}.mw-parser-output .cs1-kern-left{padding-left:0.2em}.mw-parser-output .cs1-kern-right{padding-right:0.2em}.mw-parser-output .citation .mw-selflink{font-weight:inherit}@media screen{.mw-parser-output .cs1-format{font-size:95%}html.skin-theme-clientpref-night .mw-parser-output .cs1-maint{color:#18911f}}@media screen and (prefers-color-scheme:dark){html.skin-theme-clientpref-os .mw-parser-output .cs1-maint{color:#18911f}}</style><cite id="CITEREFArpaci-DusseauArpaci-Dusseau2014" class="citation cs2">Arpaci-Dusseau, Remzi H.; Arpaci-Dusseau, Andrea C. (2014), <a rel="nofollow" class="external text" href="http://pages.cs.wisc.edu/~remzi/OSTEP/threads-cv.pdf"><i>Operating Systems: Three Easy Pieces &#91;Chapter: Condition Variables, figure 30.13&#93;</i></a> <span class="cs1-format">(PDF)</span>, Arpaci-Dusseau Books</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Abook&amp;rft.genre=book&amp;rft.btitle=Operating+Systems%3A+Three+Easy+Pieces+%5BChapter%3A+Condition+Variables%2C+figure+30.13%5D&amp;rft.pub=Arpaci-Dusseau+Books&amp;rft.date=2014&amp;rft.aulast=Arpaci-Dusseau&amp;rft.aufirst=Remzi+H.&amp;rft.au=Arpaci-Dusseau%2C+Andrea+C.&amp;rft_id=http%3A%2F%2Fpages.cs.wisc.edu%2F~remzi%2FOSTEP%2Fthreads-cv.pdf&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-2"><span class="mw-cite-backlink"><b><a href="#cite_ref-2">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFHartl2011" class="citation web cs1">Hartl, Johann (17 October 2011). <a rel="nofollow" class="external text" href="https://www.youtube.com/watch?v=_xI9tXi-UNs">"Impulswiederholer - Telephone Exchange (video)"</a>. Youtube<span class="reference-accessdate">. Retrieved <span class="nowrap">15 December</span> 2021</span>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Abook&amp;rft.genre=unknown&amp;rft.btitle=Impulswiederholer+-+Telephone+Exchange+%28video%29&amp;rft.pub=Youtube&amp;rft.date=2011-10-17&amp;rft.aulast=Hartl&amp;rft.aufirst=Johann&amp;rft_id=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3D_xI9tXi-UNs&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-3"><span class="mw-cite-backlink"><b><a href="#cite_ref-3">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFFraser" class="citation web cs1">Fraser, Alexander Gibson. <a rel="nofollow" class="external text" href="https://patents.google.com/patent/US3979733A/en">"US patent 3979733 Digital data communications system packet switch"</a>. US States Patent<span class="reference-accessdate">. Retrieved <span class="nowrap">15 December</span> 2021</span>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Abook&amp;rft.genre=unknown&amp;rft.btitle=US+patent+3979733+Digital+data+communications+system+packet+switch&amp;rft.pub=US+States+Patent&amp;rft.aulast=Fraser&amp;rft.aufirst=Alexander+Gibson&amp;rft_id=https%3A%2F%2Fpatents.google.com%2Fpatent%2FUS3979733A%2Fen&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-Liu_Wu_Das_2021_p._117-4"><span class="mw-cite-backlink"><b><a href="#cite_ref-Liu_Wu_Das_2021_p._117_4-0">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFLiuWuDas2021" class="citation book cs1">Liu, Z.; Wu, F.; Das, S.K. (2021). <a rel="nofollow" class="external text" href="https://books.google.com/books?id=si1CEAAAQBAJ&amp;pg=PA117"><i>Wireless Algorithms, Systems, and Applications: 16th International Conference, WASA 2021, Nanjing, China, June 25–27, 2021, Proceedings, Part II</i></a>. Lecture Notes in Computer Science. Springer International Publishing. p.&#160;117. <a href="/wiki/ISBN_(identifier)" class="mw-redirect" title="ISBN (identifier)">ISBN</a>&#160;<a href="/wiki/Special:BookSources/978-3-030-86130-8" title="Special:BookSources/978-3-030-86130-8"><bdi>978-3-030-86130-8</bdi></a><span class="reference-accessdate">. Retrieved <span class="nowrap">2023-09-04</span></span>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Abook&amp;rft.genre=book&amp;rft.btitle=Wireless+Algorithms%2C+Systems%2C+and+Applications%3A+16th+International+Conference%2C+WASA+2021%2C+Nanjing%2C+China%2C+June+25%E2%80%9327%2C+2021%2C+Proceedings%2C+Part+II&amp;rft.series=Lecture+Notes+in+Computer+Science&amp;rft.pages=117&amp;rft.pub=Springer+International+Publishing&amp;rft.date=2021&amp;rft.isbn=978-3-030-86130-8&amp;rft.aulast=Liu&amp;rft.aufirst=Z.&amp;rft.au=Wu%2C+F.&amp;rft.au=Das%2C+S.K.&amp;rft_id=https%3A%2F%2Fbooks.google.com%2Fbooks%3Fid%3Dsi1CEAAAQBAJ%26pg%3DPA117&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-5"><span class="mw-cite-backlink"><b><a href="#cite_ref-5">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFChandrasekaran2014" class="citation web cs1">Chandrasekaran, Siddharth (2014-05-16). <a rel="nofollow" class="external text" href="https://embedjournal.com/implementing-circular-buffer-embedded-c/">"Implementing Circular/Ring Buffer in Embedded C"</a>. <i>Embed Journal</i>. EmbedJournal Team. <a rel="nofollow" class="external text" href="https://web.archive.org/web/20170211031659/http://embedjournal.com/implementing-circular-buffer-embedded-c/">Archived</a> from the original on 11 February 2017<span class="reference-accessdate">. Retrieved <span class="nowrap">14 August</span> 2017</span>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Ajournal&amp;rft.genre=unknown&amp;rft.jtitle=Embed+Journal&amp;rft.atitle=Implementing+Circular%2FRing+Buffer+in+Embedded+C&amp;rft.date=2014-05-16&amp;rft.aulast=Chandrasekaran&amp;rft.aufirst=Siddharth&amp;rft_id=https%3A%2F%2Fembedjournal.com%2Fimplementing-circular-buffer-embedded-c%2F&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-6"><span class="mw-cite-backlink"><b><a href="#cite_ref-6">^</a></b></span> <span class="reference-text"><a rel="nofollow" class="external text" href="https://www.kernel.org/doc/Documentation/circular-buffers.txt#:~:text=A%20circular%20buffer%20is%20a,next%20item%20in%20the%20buffer">Circular buffers</a> kernel.org</span>
</li>
<li id="cite_note-7"><span class="mw-cite-backlink"><b><a href="#cite_ref-7">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFMorin" class="citation web cs1"><a href="/wiki/Pat_Morin" title="Pat Morin">Morin, Pat</a>. <a rel="nofollow" class="external text" href="http://opendatastructures.org/ods-python/2_3_ArrayQueue_Array_Based_.html">"ArrayQueue: An Array-Based Queue"</a>. <i>Open Data Structures (in pseudocode)</i>. <a rel="nofollow" class="external text" href="https://web.archive.org/web/20150831023453/http://opendatastructures.org/ods-python/2_3_ArrayQueue_Array_Based_.html">Archived</a> from the original on 31 August 2015<span class="reference-accessdate">. Retrieved <span class="nowrap">7 November</span> 2015</span>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Ajournal&amp;rft.genre=unknown&amp;rft.jtitle=Open+Data+Structures+%28in+pseudocode%29&amp;rft.atitle=ArrayQueue%3A+An+Array-Based+Queue&amp;rft.aulast=Morin&amp;rft.aufirst=Pat&amp;rft_id=http%3A%2F%2Fopendatastructures.org%2Fods-python%2F2_3_ArrayQueue_Array_Based_.html&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-8"><span class="mw-cite-backlink"><b><a href="#cite_ref-8">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFMike_Ash2012" class="citation web cs1">Mike Ash (2012-02-17). <a rel="nofollow" class="external text" href="https://www.mikeash.com/pyblog/friday-qa-2012-02-17-ring-buffers-and-mirrored-memory-part-ii.html">"mikeash.com: Friday Q&amp;A 2012-02-17: Ring Buffers and Mirrored Memory: Part II"</a>. <i>mikeash.com</i>. <a rel="nofollow" class="external text" href="https://web.archive.org/web/20190111054903/https://www.mikeash.com/pyblog/friday-qa-2012-02-17-ring-buffers-and-mirrored-memory-part-ii.html">Archived</a> from the original on 2019-01-11<span class="reference-accessdate">. Retrieved <span class="nowrap">2019-01-10</span></span>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Ajournal&amp;rft.genre=unknown&amp;rft.jtitle=mikeash.com&amp;rft.atitle=mikeash.com%3A+Friday+Q%26A+2012-02-17%3A+Ring+Buffers+and+Mirrored+Memory%3A+Part+II&amp;rft.date=2012-02-17&amp;rft.au=Mike+Ash&amp;rft_id=https%3A%2F%2Fwww.mikeash.com%2Fpyblog%2Ffriday-qa-2012-02-17-ring-buffers-and-mirrored-memory-part-ii.html&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
<li id="cite_note-cooke-9"><span class="mw-cite-backlink"><b><a href="#cite_ref-cooke_9-0">^</a></b></span> <span class="reference-text">Simon Cooke (2003), <a rel="nofollow" class="external text" href="http://www.codeproject.com/Articles/3479/The-Bip-Buffer-The-Circular-Buffer-with-a-Twist">"The Bip Buffer - The Circular Buffer with a Twist"</a></span>
</li>
<li id="cite_note-gunther-10"><span class="mw-cite-backlink"><b><a href="#cite_ref-gunther_10-0">^</a></b></span> <span class="reference-text"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1238218222" /><cite id="CITEREFGunther2014" class="citation journal cs1">Gunther, John C. (March 2014). "Algorithm 938: Compressing circular buffers". <i>ACM Transactions on Mathematical Software</i>. <b>40</b> (2): <span class="nowrap">1–</span>12. <a href="/wiki/Doi_(identifier)" class="mw-redirect" title="Doi (identifier)">doi</a>:<a rel="nofollow" class="external text" href="https://doi.org/10.1145%2F2559995">10.1145/2559995</a>. <a href="/wiki/S2CID_(identifier)" class="mw-redirect" title="S2CID (identifier)">S2CID</a>&#160;<a rel="nofollow" class="external text" href="https://api.semanticscholar.org/CorpusID:14682572">14682572</a>.</cite><span title="ctx_ver=Z39.88-2004&amp;rft_val_fmt=info%3Aofi%2Ffmt%3Akev%3Amtx%3Ajournal&amp;rft.genre=article&amp;rft.jtitle=ACM+Transactions+on+Mathematical+Software&amp;rft.atitle=Algorithm+938%3A+Compressing+circular+buffers&amp;rft.volume=40&amp;rft.issue=2&amp;rft.pages=1-12&amp;rft.date=2014-03&amp;rft_id=info%3Adoi%2F10.1145%2F2559995&amp;rft_id=https%3A%2F%2Fapi.semanticscholar.org%2FCorpusID%3A14682572%23id-name%3DS2CID&amp;rft.aulast=Gunther&amp;rft.aufirst=John+C.&amp;rfr_id=info%3Asid%2Fen.wikipedia.org%3ACircular+buffer" class="Z3988"></span></span>
</li>
</ol></div></div>
<div class="mw-heading mw-heading2"><h2 id="External_links">External links</h2><span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="/w/index.php?title=Circular_buffer&amp;action=edit&amp;section=7" title="Edit section: External links"><span>edit</span></a><span class="mw-editsection-bracket">]</span></span></div>
<ul><li><a href="https://wiki.c2.com/?CircularBuffer" class="extiw" title="c2:CircularBuffer">CircularBuffer</a> at the <a href="/wiki/Portland_Pattern_Repository" title="Portland Pattern Repository">Portland Pattern Repository</a></li>
<li>Boost:
<dl><dd><a rel="nofollow" class="external text" href="https://www.boost.org/doc/libs/release/doc/html/circular_buffer.html">Templated Circular Buffer Container</a>: <a rel="nofollow" class="external text" href="https://github.com/boostorg/circular_buffer/blob/develop/include/boost/circular_buffer/base.hpp">circular_buffer/base.hpp</a></dd>
<dd><a rel="nofollow" class="external text" href="https://www.boost.org/doc/libs/release/doc/html/thread/sds.html#thread.sds.synchronized_queues.ref.sync_bounded_queue_ref">Synchronized Bounded Queue</a>:  <a rel="nofollow" class="external text" href="https://github.com/boostorg/thread/blob/develop/include/boost/thread/concurrent_queues/sync_bounded_queue.hpp">sync_bounded_queue.hpp</a></dd></dl></li>
<li><a rel="nofollow" class="external text" href="https://www.kernel.org/doc/html/latest/core-api/circular-buffers.html">CB in Linux kernel</a></li>
<li><a rel="nofollow" class="external text" href="http://www.dspguide.com/ch28/2.htm">CB in DSP</a></li>
<li><a rel="nofollow" class="external text" href="http://www.martinbroadhurst.com/cirque-in-c.html">Circular queue in C</a> <a rel="nofollow" class="external text" href="https://web.archive.org/web/20181029235921/http://www.martinbroadhurst.com/cirque-in-c.html">Archived</a> 2018-10-29 at the <a href="/wiki/Wayback_Machine" title="Wayback Machine">Wayback Machine</a></li></ul>
<div class="navbox-styles"><style data-mw-deduplicate="TemplateStyles:r1129693374">.mw-parser-output .hlist dl,.mw-parser-output .hlist ol,.mw-parser-output .hlist ul{margin:0;padding:0}.mw-parser-output .hlist dd,.mw-parser-output .hlist dt,.mw-parser-output .hlist li{margin:0;display:inline}.mw-parser-output .hlist.inline,.mw-parser-output .hlist.inline dl,.mw-parser-output .hlist.inline ol,.mw-parser-output .hlist.inline ul,.mw-parser-output .hlist dl dl,.mw-parser-output .hlist dl ol,.mw-parser-output .hlist dl ul,.mw-parser-output .hlist ol dl,.mw-parser-output .hlist ol ol,.mw-parser-output .hlist ol ul,.mw-parser-output .hlist ul dl,.mw-parser-output .hlist ul ol,.mw-parser-output .hlist ul ul{display:inline}.mw-parser-output .hlist .mw-empty-li{display:none}.mw-parser-output .hlist dt::after{content:": "}.mw-parser-output .hlist dd::after,.mw-parser-output .hlist li::after{content:" · ";font-weight:bold}.mw-parser-output .hlist dd:last-child::after,.mw-parser-output .hlist dt:last-child::after,.mw-parser-output .hlist li:last-child::after{content:none}.mw-parser-output .hlist dd dd:first-child::before,.mw-parser-output .hlist dd dt:first-child::before,.mw-parser-output .hlist dd li:first-child::before,.mw-parser-output .hlist dt dd:first-child::before,.mw-parser-output .hlist dt dt:first-child::before,.mw-parser-output .hlist dt li:first-child::before,.mw-parser-output .hlist li dd:first-child::before,.mw-parser-output .hlist li dt:first-child::before,.mw-parser-output .hlist li li:first-child::before{content:" (";font-weight:normal}.mw-parser-output .hlist dd dd:last-child::after,.mw-parser-output .hlist dd dt:last-child::after,.mw-parser-output .hlist dd li:last-child::after,.mw-parser-output .hlist dt dd:last-child::after,.mw-parser-output .hlist dt dt:last-child::after,.mw-parser-output .hlist dt li:last-child::after,.mw-parser-output .hlist li dd:last-child::after,.mw-parser-output .hlist li dt:last-child::after,.mw-parser-output .hlist li li:last-child::after{content:")";font-weight:normal}.mw-parser-output .hlist ol{counter-reset:listitem}.mw-parser-output .hlist ol>li{counter-increment:listitem}.mw-parser-output .hlist ol>li::before{content:" "counter(listitem)"\a0 "}.mw-parser-output .hlist dd ol>li:first-child::before,.mw-parser-output .hlist dt ol>li:first-child::before,.mw-parser-output .hlist li ol>li:first-child::before{content:" ("counter(listitem)"\a0 "}</style><style data-mw-deduplicate="TemplateStyles:r1236075235">.mw-parser-output .navbox{box-sizing:border-box;border:1px solid #a2a9b1;width:100%;clear:both;font-size:88%;text-align:center;padding:1px;margin:1em auto 0}.mw-parser-output .navbox .navbox{margin-top:0}.mw-parser-output .navbox+.navbox,.mw-parser-output .navbox+.navbox-styles+.navbox{margin-top:-1px}.mw-parser-output .navbox-inner,.mw-parser-output .navbox-subgroup{width:100%}.mw-parser-output .navbox-group,.mw-parser-output .navbox-title,.mw-parser-output .navbox-abovebelow{padding:0.25em 1em;line-height:1.5em;text-align:center}.mw-parser-output .navbox-group{white-space:nowrap;text-align:right}.mw-parser-output .navbox,.mw-parser-output .navbox-subgroup{background-color:#fdfdfd}.mw-parser-output .navbox-list{line-height:1.5em;border-color:#fdfdfd}.mw-parser-output .navbox-list-with-group{text-align:left;border-left-width:2px;border-left-style:solid}.mw-parser-output tr+tr>.navbox-abovebelow,.mw-parser-output tr+tr>.navbox-group,.mw-parser-output tr+tr>.navbox-image,.mw-parser-output tr+tr>.navbox-list{border-top:2px solid #fdfdfd}.mw-parser-output .navbox-title{background-color:#ccf}.mw-parser-output .navbox-abovebelow,.mw-parser-output .navbox-group,.mw-parser-output .navbox-subgroup .navbox-title{background-color:#ddf}.mw-parser-output .navbox-subgroup .navbox-group,.mw-parser-output .navbox-subgroup .navbox-abovebelow{background-color:#e6e6ff}.mw-parser-output .navbox-even{background-color:#f7f7f7}.mw-parser-output .navbox-odd{background-color:transparent}.mw-parser-output .navbox .hlist td dl,.mw-parser-output .navbox .hlist td ol,.mw-parser-output .navbox .hlist td ul,.mw-parser-output .navbox td.hlist dl,.mw-parser-output .navbox td.hlist ol,.mw-parser-output .navbox td.hlist ul{padding:0.125em 0}.mw-parser-output .navbox .navbar{display:block;font-size:100%}.mw-parser-output .navbox-title .navbar{float:left;text-align:left;margin-right:0.5em}body.skin--responsive .mw-parser-output .navbox-image img{max-width:none!important}@media print{body.ns-0 .mw-parser-output .navbox{display:none!important}}</style></div><div role="navigation" class="navbox" aria-labelledby="Data_structures1458" style="padding:3px"><table class="nowraplinks hlist mw-collapsible autocollapse navbox-inner" style="border-spacing:0;background:transparent;color:inherit"><tbody><tr><th scope="col" class="navbox-title" colspan="2"><link rel="mw-deduplicated-inline-style" href="mw-data:TemplateStyles:r1129693374" /><style data-mw-deduplicate="TemplateStyles:r1239400231">.mw-parser-output .navbar{display:inline;font-size:88%;font-weight:normal}.mw-parser-output .navbar-collapse{float:left;text-align:left}.mw-parser-output .navbar-boxtext{word-spacing:0}.mw-parser-output .navbar ul{display:inline-block;white-space:nowrap;line-height:inherit}.mw-parser-output .navbar-brackets::before{margin-right:-0.125em;content:"[ "}.mw-parser-output .navbar-brackets::after{margin-left:-0.125em;content:" ]"}.mw-parser-output .navbar li{word-spacing:-0.125em}.mw-parser-output .navbar a>span,.mw-parser-output .navbar a>abbr{text-decoration:inherit}.mw-parser-output .navbar-mini abbr{font-variant:small-caps;border-bottom:none;text-decoration:none;cursor:inherit}.mw-parser-output .navbar-ct-full{font-size:114%;margin:0 7em}.mw-parser-output .navbar-ct-mini{font-size:114%;margin:0 4em}html.skin-theme-clientpref-night .mw-parser-output .navbar li a abbr{color:var(--color-base)!important}@media(prefers-color-scheme:dark){html.skin-theme-clientpref-os .mw-parser-output .navbar li a abbr{color:var(--color-base)!important}}@media print{.mw-parser-output .navbar{display:none!important}}</style><div class="navbar plainlinks hlist navbar-mini"><ul><li class="nv-view"><a href="/wiki/Template:Data_structures" title="Template:Data structures"><abbr title="View this template">v</abbr></a></li><li class="nv-talk"><a href="/wiki/Template_talk:Data_structures" title="Template talk:Data structures"><abbr title="Discuss this template">t</abbr></a></li><li class="nv-edit"><a href="/wiki/Special:EditPage/Template:Data_structures" title="Special:EditPage/Template:Data structures"><abbr title="Edit this template">e</abbr></a></li></ul></div><div id="Data_structures1458" style="font-size:114%;margin:0 4em"><a href="/wiki/Data_structure" title="Data structure">Data structures</a></div></th></tr><tr><th scope="row" class="navbox-group" style="width:1%">Types</th><td class="navbox-list-with-group navbox-list navbox-odd" style="width:100%;padding:0"><div style="padding:0 0.25em">
<ul><li><a href="/wiki/Collection_(abstract_data_type)" title="Collection (abstract data type)">Collection</a></li>
<li><a href="/wiki/Container_(abstract_data_type)" title="Container (abstract data type)">Container</a></li></ul>
</div></td></tr><tr><th scope="row" class="navbox-group" style="width:1%"><a href="/wiki/Abstract_data_type" title="Abstract data type">Abstract</a></th><td class="navbox-list-with-group navbox-list navbox-even" style="width:100%;padding:0"><div style="padding:0 0.25em">
<ul><li><a href="/wiki/Associative_array" title="Associative array">Associative array</a>
<ul><li><a href="/wiki/Multimap" title="Multimap">Multimap</a></li>
<li><a href="/wiki/Retrieval_Data_Structure" title="Retrieval Data Structure">Retrieval Data Structure</a></li></ul></li>
<li><a href="/wiki/List_(abstract_data_type)" title="List (abstract data type)">List</a></li>
<li><a href="/wiki/Stack_(abstract_data_type)" title="Stack (abstract data type)">Stack</a></li>
<li><a href="/wiki/Queue_(abstract_data_type)" title="Queue (abstract data type)">Queue</a>
<ul><li><a href="/wiki/Double-ended_queue" title="Double-ended queue">Double-ended queue</a></li></ul></li>
<li><a href="/wiki/Priority_queue" title="Priority queue">Priority queue</a>
<ul><li><a href="/wiki/Double-ended_priority_queue" title="Double-ended priority queue">Double-ended priority queue</a></li></ul></li>
<li><a href="/wiki/Set_(abstract_data_type)" title="Set (abstract data type)">Set</a>
<ul><li><a href="/wiki/Set_(abstract_data_type)#Multiset" title="Set (abstract data type)">Multiset</a></li>
<li><a href="/wiki/Disjoint-set_data_structure" title="Disjoint-set data structure">Disjoint-set</a></li></ul></li></ul>
</div></td></tr><tr><th scope="row" class="navbox-group" style="width:1%"><a href="/wiki/Array_(data_structure)" title="Array (data structure)">Arrays</a></th><td class="navbox-list-with-group navbox-list navbox-odd" style="width:100%;padding:0"><div style="padding:0 0.25em">
<ul><li><a href="/wiki/Bit_array" title="Bit array">Bit array</a></li>
<li><a class="mw-selflink selflink">Circular buffer</a></li>
<li><a href="/wiki/Dynamic_array" title="Dynamic array">Dynamic array</a></li>
<li><a href="/wiki/Hash_table" title="Hash table">Hash table</a></li>
<li><a href="/wiki/Hashed_array_tree" title="Hashed array tree">Hashed array tree</a></li>
<li><a href="/wiki/Sparse_matrix" title="Sparse matrix">Sparse matrix</a></li></ul>
</div></td></tr><tr><th scope="row" class="navbox-group" style="width:1%"><a href="/wiki/Linked_data_structure" title="Linked data structure">Linked</a></th><td class="navbox-list-with-group navbox-list navbox-even" style="width:100%;padding:0"><div style="padding:0 0.25em">
<ul><li><a href="/wiki/Association_list" title="Association list">Association list</a></li>
<li><a href="/wiki/Linked_list" title="Linked list">Linked list</a></li>
<li><a href="/wiki/Skip_list" title="Skip list">Skip list</a></li>
<li><a href="/wiki/Unrolled_linked_list" title="Unrolled linked list">Unrolled linked list</a></li>
<li><a href="/wiki/XOR_linked_list" title="XOR linked list">XOR linked list</a></li></ul>
</div></td></tr><tr><th scope="row" class="navbox-group" style="width:1%"><a href="/wiki/Tree_(data_structure)" class="mw-redirect" title="Tree (data structure)">Trees</a></th><td class="navbox-list-with-group navbox-list navbox-odd" style="width:100%;padding:0"><div style="padding:0 0.25em">
<ul><li><a href="/wiki/B-tree" title="B-tree">B-tree</a></li>
<li><a href="/wiki/Binary_search_tree" title="Binary search tree">Binary search tree</a>
<ul><li><a href="/wiki/AA_tree" title="AA tree">AA tree</a></li>
<li><a href="/wiki/AVL_tree" title="AVL tree">AVL tree</a></li>
<li><a href="/wiki/Red%E2%80%93black_tree" title="Red–black tree">Red–black tree</a></li>
<li><a href="/wiki/Self-balancing_binary_search_tree" title="Self-balancing binary search tree">Self-balancing tree</a></li>
<li><a href="/wiki/Splay_tree" title="Splay tree">Splay tree</a></li></ul></li>
<li><a href="/wiki/Heap_(data_structure)" title="Heap (data structure)">Heap</a>
<ul><li><a href="/wiki/Binary_heap" title="Binary heap">Binary heap</a></li>
<li><a href="/wiki/Binomial_heap" title="Binomial heap">Binomial heap</a></li>
<li><a href="/wiki/Fibonacci_heap" title="Fibonacci heap">Fibonacci heap</a></li></ul></li>
<li><a href="/wiki/R-tree" title="R-tree">R-tree</a>
<ul><li><a href="/wiki/R*_tree" class="mw-redirect" title="R* tree">R* tree</a></li>
<li><a href="/wiki/R%2B_tree" title="R+ tree">R+ tree</a></li>
<li><a href="/wiki/Hilbert_R-tree" title="Hilbert R-tree">Hilbert R-tree</a></li></ul></li>
<li><a href="/wiki/Trie" title="Trie">Trie</a>
<ul><li><a href="/wiki/Hash_tree_(persistent_data_structure)" title="Hash tree (persistent data structure)">Hash tree</a></li></ul></li></ul>
</div></td></tr><tr><th scope="row" class="navbox-group" style="width:1%"><a href="/wiki/Graph_(abstract_data_type)" title="Graph (abstract data type)">Graphs</a></th><td class="navbox-list-with-group navbox-list navbox-even" style="width:100%;padding:0"><div style="padding:0 0.25em">
<ul><li><a href="/wiki/Binary_decision_diagram" title="Binary decision diagram">Binary decision diagram</a></li>
<li><a href="/wiki/Directed_acyclic_graph" title="Directed acyclic graph">Directed acyclic graph</a></li>
<li><a href="/wiki/Deterministic_acyclic_finite_state_automaton" title="Deterministic acyclic finite state automaton">Directed acyclic word graph</a></li></ul>
</div></td></tr><tr><td class="navbox-abovebelow" colspan="2"><div>
<ul><li><a href="/wiki/List_of_data_structures" title="List of data structures">List of data structures</a></li></ul>
</div></td></tr></tbody></table></div>
<!-- 
NewPP limit report
Parsed by mw‐web.eqiad.main‐69794d664f‐b7xzq
Cached time: 20250914171546
Cache expiry: 2592000
Reduced expiry: false
Complications: [vary‐revision‐sha1, show‐toc]
CPU time usage: 0.339 seconds
Real time usage: 0.443 seconds
Preprocessor visited node count: 912/1000000
Revision size: 13261/2097152 bytes
Post‐expand include size: 29830/2097152 bytes
Template argument size: 1122/2097152 bytes
Highest expansion depth: 11/100
Expensive parser function count: 3/500
Unstrip recursion depth: 1/20
Unstrip post‐expand size: 45229/5000000 bytes
Lua time usage: 0.205/10.000 seconds
Lua memory usage: 5729969/52428800 bytes
Number of Wikibase entities loaded: 0/500
-->
<!--
Transclusion expansion time report (%,ms,calls,template)
100.00%  363.563      1 -total
 37.40%  135.984      1 Template:Reflist
 24.21%   88.012      1 Template:Data_structures
 23.50%   85.431      1 Template:Navbox
 23.03%   83.733      1 Template:Short_description
 21.62%   78.609      1 Template:Citation
 14.00%   50.895      2 Template:Pagetype
 10.80%   39.263      1 Template:Disputed_inline
  9.05%   32.905      1 Template:Fix
  6.42%   23.346      5 Template:Cite_web
-->

<!-- Saved in parser cache with key enwiki:pcache:11891734:|#|:idhash:canonical and timestamp 20250914171546 and revision id 1284869780. Rendering was triggered because: page_view
 -->
</div><noscript><img src="https://en.wikipedia.org/wiki/Special:CentralAutoLogin/start?type=1x1&amp;usesul3=1" alt="" width="1" height="1" style="border: none; position: absolute;"></noscript>
<div class="printfooter" data-nosnippet="">Retrieved from "<a dir="ltr" href="https://en.wikipedia.org/w/index.php?title=Circular_buffer&amp;oldid=1284869780">https://en.wikipedia.org/w/index.php?title=Circular_buffer&amp;oldid=1284869780</a>"</div></div>
					<div id="catlinks" class="catlinks" data-mw="interface"><div id="mw-normal-catlinks" class="mw-normal-catlinks"><a href="/wiki/Help:Category" title="Help:Category">Categories</a>: <ul><li><a href="/wiki/Category:Computer_memory" title="Category:Computer memory">Computer memory</a></li><li><a href="/wiki/Category:Arrays" title="Category:Arrays">Arrays</a></li></ul></div><div id="mw-hidden-catlinks" class="mw-hidden-catlinks mw-hidden-cats-hidden">Hidden categories: <ul><li><a href="/wiki/Category:Articles_with_short_description" title="Category:Articles with short description">Articles with short description</a></li><li><a href="/wiki/Category:Short_description_is_different_from_Wikidata" title="Category:Short description is different from Wikidata">Short description is different from Wikidata</a></li><li><a href="/wiki/Category:All_accuracy_disputes" title="Category:All accuracy disputes">All accuracy disputes</a></li><li><a href="/wiki/Category:Articles_with_disputed_statements_from_January_2022" title="Category:Articles with disputed statements from January 2022">Articles with disputed statements from January 2022</a></li><li><a href="/wiki/Category:Webarchive_template_wayback_links" title="Category:Webarchive template wayback links">Webarchive template wayback links</a></li></ul></div></div>
				</div>
			</main>
			
		</div>
		<div class="mw-footer-container">
			
<footer id="footer" class="mw-footer" >
	<ul id="footer-info">
	<li id="footer-info-lastmod"> This page was last edited on 10 April 2025, at 06:43<span class="anonymous-show">&#160;(UTC)</span>.</li>
	<li id="footer-info-copyright">Text is available under the <a href="/wiki/Wikipedia:Text_of_the_Creative_Commons_Attribution-ShareAlike_4.0_International_License" title="Wikipedia:Text of the Creative Commons Attribution-ShareAlike 4.0 International License">Creative Commons Attribution-ShareAlike 4.0 License</a>;
additional terms may apply. By using this site, you agree to the <a href="https://foundation.wikimedia.org/wiki/Special:MyLanguage/Policy:Terms_of_Use" class="extiw" title="foundation:Special:MyLanguage/Policy:Terms of Use">Terms of Use</a> and <a href="https://foundation.wikimedia.org/wiki/Special:MyLanguage/Policy:Privacy_policy" class="extiw" title="foundation:Special:MyLanguage/Policy:Privacy policy">Privacy Policy</a>. Wikipedia® is a registered trademark of the <a rel="nofollow" class="external text" href="https://wikimediafoundation.org/">Wikimedia Foundation, Inc.</a>, a non-profit organization.</li>
</ul>

	<ul id="footer-places">
	<li id="footer-places-privacy"><a href="https://foundation.wikimedia.org/wiki/Special:MyLanguage/Policy:Privacy_policy">Privacy policy</a></li>
	<li id="footer-places-about"><a href="/wiki/Wikipedia:About">About Wikipedia</a></li>
	<li id="footer-places-disclaimers"><a href="/wiki/Wikipedia:General_disclaimer">Disclaimers</a></li>
	<li id="footer-places-contact"><a href="//en.wikipedia.org/wiki/Wikipedia:Contact_us">Contact Wikipedia</a></li>
	<li id="footer-places-wm-codeofconduct"><a href="https://foundation.wikimedia.org/wiki/Special:MyLanguage/Policy:Universal_Code_of_Conduct">Code of Conduct</a></li>
	<li id="footer-places-developers"><a href="https://developer.wikimedia.org">Developers</a></li>
	<li id="footer-places-statslink"><a href="https://stats.wikimedia.org/#/en.wikipedia.org">Statistics</a></li>
	<li id="footer-places-cookiestatement"><a href="https://foundation.wikimedia.org/wiki/Special:MyLanguage/Policy:Cookie_statement">Cookie statement</a></li>
	<li id="footer-places-mobileview"><a href="//en.m.wikipedia.org/w/index.php?title=Circular_buffer&amp;mobileaction=toggle_view_mobile" class="noprint stopMobileRedirectToggle">Mobile view</a></li>
</ul>

	<ul id="footer-icons" class="noprint">
	<li id="footer-copyrightico"><a href="https://www.wikimedia.org/" class="cdx-button cdx-button--fake-button cdx-button--size-large cdx-button--fake-button--enabled"><picture><source media="(min-width: 500px)" srcset="/static/images/footer/wikimedia-button.svg" width="84" height="29"><img src="/static/images/footer/wikimedia.svg" width="25" height="25" alt="Wikimedia Foundation" lang="en" loading="lazy"></picture></a></li>
	<li id="footer-poweredbyico"><a href="https://www.mediawiki.org/" class="cdx-button cdx-button--fake-button cdx-button--size-large cdx-button--fake-button--enabled"><picture><source media="(min-width: 500px)" srcset="/w/resources/assets/poweredby_mediawiki.svg" width="88" height="31"><img src="/w/resources/assets/mediawiki_compact.svg" alt="Powered by MediaWiki" lang="en" width="25" height="25" loading="lazy"></picture></a></li>
</ul>

</footer>

		</div>
	</div> 
</div> 
<div class="vector-header-container vector-sticky-header-container no-font-mode-scale">
	<div id="vector-sticky-header" class="vector-sticky-header">
		<div class="vector-sticky-header-start">
			<div class="vector-sticky-header-icon-start vector-button-flush-left vector-button-flush-right" aria-hidden="true">
				<button class="cdx-button cdx-button--weight-quiet cdx-button--icon-only vector-sticky-header-search-toggle" tabindex="-1" data-event-name="ui.vector-sticky-search-form.icon"><span class="vector-icon mw-ui-icon-search mw-ui-icon-wikimedia-search"></span>

<span>Search</span>
			</button>
		</div>
			
		<div role="search" class="vector-search-box-vue  vector-search-box-show-thumbnail vector-search-box">
			<div class="vector-typeahead-search-container">
				<div class="cdx-typeahead-search cdx-typeahead-search--show-thumbnail">
					<form action="/w/index.php" id="vector-sticky-search-form" class="cdx-search-input cdx-search-input--has-end-button">
						<div  class="cdx-search-input__input-wrapper"  data-search-loc="header-moved">
							<div class="cdx-text-input cdx-text-input--has-start-icon">
								<input
									class="cdx-text-input__input mw-searchInput" autocomplete="off"
									
									type="search" name="search" placeholder="Search Wikipedia">
								<span class="cdx-text-input__icon cdx-text-input__start-icon"></span>
							</div>
							<input type="hidden" name="title" value="Special:Search">
						</div>
						<button class="cdx-button cdx-search-input__end-button">Search</button>
					</form>
				</div>
			</div>
		</div>
		<div class="vector-sticky-header-context-bar">
				<nav aria-label="Contents" class="vector-toc-landmark">
						
					<div id="vector-sticky-header-toc" class="vector-dropdown mw-portlet mw-portlet-sticky-header-toc vector-sticky-header-toc vector-button-flush-left"  >
						<input type="checkbox" id="vector-sticky-header-toc-checkbox" role="button" aria-haspopup="true" data-event-name="ui.dropdown-vector-sticky-header-toc" class="vector-dropdown-checkbox "  aria-label="Toggle the table of contents"  >
						<label id="vector-sticky-header-toc-label" for="vector-sticky-header-toc-checkbox" class="vector-dropdown-label cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only " aria-hidden="true"  ><span class="vector-icon mw-ui-icon-listBullet mw-ui-icon-wikimedia-listBullet"></span>

<span class="vector-dropdown-label-text">Toggle the table of contents</span>
						</label>
						<div class="vector-dropdown-content">
					
						<div id="vector-sticky-header-toc-unpinned-container" class="vector-unpinned-container">
						</div>
					
						</div>
					</div>
			</nav>
				<div class="vector-sticky-header-context-bar-primary" aria-hidden="true" ><span class="mw-page-title-main">Circular buffer</span></div>
			</div>
		</div>
		<div class="vector-sticky-header-end" aria-hidden="true">
			<div class="vector-sticky-header-icons">
				<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only" id="ca-talk-sticky-header" tabindex="-1" data-event-name="talk-sticky-header"><span class="vector-icon mw-ui-icon-speechBubbles mw-ui-icon-wikimedia-speechBubbles"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only" id="ca-subject-sticky-header" tabindex="-1" data-event-name="subject-sticky-header"><span class="vector-icon mw-ui-icon-article mw-ui-icon-wikimedia-article"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only" id="ca-history-sticky-header" tabindex="-1" data-event-name="history-sticky-header"><span class="vector-icon mw-ui-icon-wikimedia-history mw-ui-icon-wikimedia-wikimedia-history"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only mw-watchlink" id="ca-watchstar-sticky-header" tabindex="-1" data-event-name="watch-sticky-header"><span class="vector-icon mw-ui-icon-wikimedia-star mw-ui-icon-wikimedia-wikimedia-star"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only reading-lists-bookmark" id="ca-bookmark-sticky-header" tabindex="-1" data-event-name="watch-sticky-bookmark"><span class="vector-icon mw-ui-icon-wikimedia-bookmarkOutline mw-ui-icon-wikimedia-wikimedia-bookmarkOutline"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only" id="ca-edit-sticky-header" tabindex="-1" data-event-name="wikitext-edit-sticky-header"><span class="vector-icon mw-ui-icon-wikimedia-wikiText mw-ui-icon-wikimedia-wikimedia-wikiText"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only" id="ca-ve-edit-sticky-header" tabindex="-1" data-event-name="ve-edit-sticky-header"><span class="vector-icon mw-ui-icon-wikimedia-edit mw-ui-icon-wikimedia-wikimedia-edit"></span>

<span></span>
			</a>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--icon-only" id="ca-viewsource-sticky-header" tabindex="-1" data-event-name="ve-edit-protected-sticky-header"><span class="vector-icon mw-ui-icon-wikimedia-editLock mw-ui-icon-wikimedia-wikimedia-editLock"></span>

<span></span>
			</a>
		</div>
			<div class="vector-sticky-header-buttons">
				<button class="cdx-button cdx-button--weight-quiet mw-interlanguage-selector" id="p-lang-btn-sticky-header" tabindex="-1" data-event-name="ui.dropdown-p-lang-btn-sticky-header"><span class="vector-icon mw-ui-icon-wikimedia-language mw-ui-icon-wikimedia-wikimedia-language"></span>

<span>18 languages</span>
			</button>
			<a href="#" class="cdx-button cdx-button--fake-button cdx-button--fake-button--enabled cdx-button--weight-quiet cdx-button--action-progressive" id="ca-addsection-sticky-header" tabindex="-1" data-event-name="addsection-sticky-header"><span class="vector-icon mw-ui-icon-speechBubbleAdd-progressive mw-ui-icon-wikimedia-speechBubbleAdd-progressive"></span>

<span>Add topic</span>
			</a>
		</div>
			<div class="vector-sticky-header-icon-end">
				<div class="vector-user-links">
				</div>
			</div>
		</div>
	</div>
</div>
<div class="mw-portlet mw-portlet-dock-bottom emptyPortlet" id="p-dock-bottom">
	<ul>
		
	</ul>
</div>
<script>(RLQ=window.RLQ||[]).push(function(){mw.config.set({"wgHostname":"mw-web.eqiad.main-69794d664f-b7xzq","wgBackendResponseTime":605,"wgPageParseReport":{"limitreport":{"cputime":"0.339","walltime":"0.443","ppvisitednodes":{"value":912,"limit":1000000},"revisionsize":{"value":13261,"limit":2097152},"postexpandincludesize":{"value":29830,"limit":2097152},"templateargumentsize":{"value":1122,"limit":2097152},"expansiondepth":{"value":11,"limit":100},"expensivefunctioncount":{"value":3,"limit":500},"unstrip-depth":{"value":1,"limit":20},"unstrip-size":{"value":45229,"limit":5000000},"entityaccesscount":{"value":0,"limit":500},"timingprofile":["100.00%  363.563      1 -total"," 37.40%  135.984      1 Template:Reflist"," 24.21%   88.012      1 Template:Data_structures"," 23.50%   85.431      1 Template:Navbox"," 23.03%   83.733      1 Template:Short_description"," 21.62%   78.609      1 Template:Citation"," 14.00%   50.895      2 Template:Pagetype"," 10.80%   39.263      1 Template:Disputed_inline","  9.05%   32.905      1 Template:Fix","  6.42%   23.346      5 Template:Cite_web"]},"scribunto":{"limitreport-timeusage":{"value":"0.205","limit":"10.000"},"limitreport-memusage":{"value":5729969,"limit":52428800}},"cachereport":{"origin":"mw-web.eqiad.main-69794d664f-b7xzq","timestamp":"20250914171546","ttl":2592000,"transientcontent":false}}});});</script>
<script type="application/ld+json">{"@context":"https:\/\/schema.org","@type":"Article","name":"Circular buffer","url":"https:\/\/en.wikipedia.org\/wiki\/Circular_buffer","sameAs":"http:\/\/www.wikidata.org\/entity\/Q1224994","mainEntity":"http:\/\/www.wikidata.org\/entity\/Q1224994","author":{"@type":"Organization","name":"Contributors to Wikimedia projects"},"publisher":{"@type":"Organization","name":"Wikimedia Foundation, Inc.","logo":{"@type":"ImageObject","url":"https:\/\/www.wikimedia.org\/static\/images\/wmf-hor-googpub.png"}},"datePublished":"2007-06-22T01:20:50Z","dateModified":"2025-04-10T06:43:14Z","image":"https:\/\/upload.wikimedia.org\/wikipedia\/commons\/b\/b7\/Circular_buffer.svg","headline":"data structure"}</script>
</body>
</html>