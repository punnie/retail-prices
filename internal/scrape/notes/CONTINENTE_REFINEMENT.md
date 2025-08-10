# Continente Scraper Refinement

## Overview

The Continente scraper has been significantly refined to use a sitemap-based approach for comprehensive and reliable product discovery. This approach leverages Continente's official sitemap.xml to discover all available products systematically.

## Key Changes Made

### 1. Enhanced Base Scraper (`internal/scraper/base.go`)

**Added XML Parsing Capabilities:**
- `SitemapIndex` struct for parsing sitemap index XML
- `URLSet` struct for parsing individual sitemap XML files
- `FetchXML()` method for retrieving XML documents
- `ParseSitemapIndex()` method for parsing sitemap indexes
- `ParseSitemap()` method for parsing individual sitemaps

### 2. Completely Rewritten Continente Scraper (`internal/scraper/continente.go`)

**New Sitemap-Based Architecture:**
- Uses `https://www.continente.pt/sitemap.xml` as the entry point
- Automatically discovers all product sitemaps (e.g., `sitemap-custom_sitemap_1-product.xml`)
- Extracts individual product URLs from each sitemap
- Scrapes detailed product information from individual product pages

**Key Features:**
- **Comprehensive Product Discovery**: Uses official sitemaps to find all products
- **Intelligent Filtering**: Automatically identifies product sitemaps vs. other types
- **Rate Limiting**: Built-in delays and limits to be respectful to the server
- **Error Handling**: Robust error handling with graceful degradation
- **Fallback Support**: Falls back to search-based approach when needed

### 3. Enhanced Product Extraction

**Improved Selectors:**
- More comprehensive CSS selectors for product names, prices, images, brands
- Meta tag fallbacks for better data extraction
- Category extraction from breadcrumbs
- Multiple fallback strategies for each data point

**Data Quality:**
- Better price parsing with Portuguese decimal format support
- Comprehensive text cleaning and normalization
- URL resolution for relative links

### 4. Built-in Safeguards

**Performance Limits:**
- Maximum 20 products per sitemap (configurable)
- Overall limit of 100 products per run (configurable)
- 1-second delay between requests (configurable)

**Error Resilience:**
- Continues processing even if individual products fail
- Logs warnings for failed operations
- Graceful handling of network errors

## Technical Implementation

### Sitemap Processing Flow

1. **Fetch Sitemap Index**: Retrieves the main sitemap.xml
2. **Filter Product Sitemaps**: Identifies sitemaps containing "product" in their URL
3. **Process Each Sitemap**: 
   - Fetches individual product sitemap XML
   - Extracts all product URLs
   - Limits processing to avoid overwhelming the system
4. **Scrape Product Pages**:
   - Fetches individual product pages
   - Extracts comprehensive product information
   - Stores in database with proper deduplication

### Data Extraction Strategy

**Product Name Extraction:**
```go
nameSelectors := []string{
    "h1.pdp-product-name",
    "h1[data-testid='pdp-product-name']",
    ".product-name h1",
    // ... more selectors with fallbacks
}
```

**Price Extraction:**
```go
priceSelectors := []string{
    ".pdp-product-price .value",
    ".product-price .value",
    ".price-current",
    // ... more selectors with fallbacks
}
```

## Testing

### New Test Coverage

**XML Parsing Tests (`internal/scraper/xml_test.go`):**
- `TestParseSitemapIndex()`: Tests sitemap index XML parsing
- `TestParseSitemap()`: Tests individual sitemap XML parsing
- `TestFilterProductSitemaps()`: Tests product sitemap filtering logic

**Existing Tests:**
- All existing price parsing and text cleaning tests continue to pass
- Base scraper functionality remains fully tested

## Usage Examples

### Basic Usage (Sitemap-based)
```bash
./price-scraper continente
```
This will:
1. Fetch the sitemap index
2. Process all product sitemaps
3. Extract up to 100 products total
4. Store in SQLite database

### Search-based Usage (Fallback)
```bash
./price-scraper continente "leite"
```
This will:
1. Use the traditional search approach
2. Search for products containing "leite"
3. Extract products from search results

## Benefits of the New Approach

### 1. Reliability
- Uses official website structure (sitemaps)
- Less likely to break with website changes
- Comprehensive product coverage

### 2. Efficiency
- Systematic discovery of all products
- No need to guess category URLs
- Automatic updates when new products are added

### 3. Respectful Scraping
- Built-in rate limiting
- Reasonable request volumes
- Follows website's intended structure

### 4. Maintainability
- Clear separation of concerns
- Extensible architecture
- Comprehensive error handling

## Configuration Options

The scraper includes several configurable limits:

```go
maxProducts := 20 // Limit per sitemap
totalLimit := 100 // Overall product limit
delay := time.Second // Delay between requests
```

These can be adjusted based on needs and server capacity.

## Future Enhancements

### Potential Improvements
1. **Dynamic Limits**: Adjust limits based on server response times
2. **Caching**: Cache sitemap data to avoid repeated fetches
3. **Parallel Processing**: Process multiple sitemaps concurrently
4. **Product Updates**: Track product changes over time
5. **Category Mapping**: Enhanced category extraction and mapping

### Extension Points
- Easy to add new product data fields
- Configurable extraction selectors
- Pluggable rate limiting strategies
- Custom error handling policies

## Conclusion

The refined Continente scraper represents a significant improvement in reliability, comprehensiveness, and maintainability. By leveraging the official sitemap structure, it provides a robust foundation for systematic product price tracking while respecting the website's intended usage patterns.
